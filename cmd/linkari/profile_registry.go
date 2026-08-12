package main

// EPIC-264: profile template demand closure.
//
// Failure class (infra-knowledge review, 2026-08-12): "Undeclared Demand /
// Verified Supply" — the binary's required template names existed only as
// call-site string literals, so every guard quantified over the supply
// artifact (profiles/ embed dir) and was vacuously satisfied by the exact
// condition that causes outages: a demanded name that was never embedded.
//
// This file is the declared demand set. Rules:
//
//  1. Every profile name passed to loadProfileTemplate* by production code
//     MUST be either a Profile* constant below or a runtime-classified
//     profile name (which falls back to ProfileDefault/ProfileEng).
//  2. Every constant MUST appear in RequiredProfiles with every mode a call
//     site demands it in.
//  3. TestProfileClosureEmbeddedSupply enforces that every RequiredProfiles
//     entry loads, validates, and renders from the embedded supply alone.
//  4. TestNoLiteralProfileNamesOutsideRegistry rejects new literal names at
//     loadProfileTemplate* call sites.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
)

// Named profile constants — the only sanctioned spellings of statically
// demanded template names.
const (
	// ProfileDefault is the zero-config classification fallback (config.go).
	ProfileDefault = "default"
	// ProfileEng is the domain-heuristic classification fallback (handler.go).
	ProfileEng = "eng"
	// ProfileVnoteTriage is the audio rubric-scoring fallback (server_score.go).
	ProfileVnoteTriage = "vnote_triage"
	// ProfileVnoteSynopsis is the voice-note synopsis prompt whose output
	// becomes the FCM notification body (server_score.go).
	ProfileVnoteSynopsis = "vnote_synopsis"
)

// ProfileDemand is one (mode, render-path) pair a call site demands.
type ProfileDemand struct {
	// Mode is the content mode passed to RenderForMode*; "" means the
	// modeless Render()/RenderForJSON() path (loadProfileTemplate[JSON]).
	Mode string
	// JSON selects the RenderForJSON/RenderForModeJSON render path.
	JSON bool
}

// RequiredProfiles is the declared demand set: every profile name the binary
// demands at a call site, in every mode it is demanded in. The embedded
// supply (profiles/) must satisfy this map completely — enforced at build
// time by TestProfileClosureEmbeddedSupply and at startup by
// ValidateProfileClosure.
//
// Demand provenance:
//   - "" / JSON       — cmd_triage.go:98,961; cmd_score.go:258; cmd_eval.go:204; server_score.go:952,2704
//   - "url" JSON      — youtube.go:898
//   - "vision" JSON   — server_score.go:950
//   - "audio" JSON    — server_score.go:2645,2655
var RequiredProfiles = map[string][]ProfileDemand{
	ProfileDefault: {
		{Mode: "", JSON: false},
		{Mode: "", JSON: true},
		{Mode: "url", JSON: true},
		{Mode: "vision", JSON: true},
		{Mode: "audio", JSON: true},
	},
	ProfileEng: {
		{Mode: "", JSON: false},
		{Mode: "", JSON: true},
		{Mode: "url", JSON: true},
		{Mode: "vision", JSON: true},
		{Mode: "audio", JSON: true},
	},
	ProfileVnoteTriage: {
		{Mode: "audio", JSON: true},
	},
	ProfileVnoteSynopsis: {
		{Mode: "", JSON: true},
	},
}

// renderEmbeddedProfileDemand loads one required profile from the embedded
// supply alone and renders it for one demand. Used by the demand-side
// closure test and by ValidateProfileClosure. A YAML manifest is validated
// and rendered; a raw .md prompt (e.g. vnote_synopsis) only needs to exist
// non-empty — it has no mode dimension.
func renderEmbeddedProfileDemand(name string, d ProfileDemand) error {
	if b, err := fs.ReadFile(EmbeddedProfileFS(), name+".yaml"); err == nil {
		m, lerr := LoadProfileManifestBytes(b, "embedded:"+name+".yaml")
		if lerr != nil {
			return fmt.Errorf("embedded %s.yaml invalid: %w", name, lerr)
		}
		var rendered string
		var rerr error
		switch {
		case d.Mode == "" && d.JSON:
			rendered, rerr = m.RenderForJSON()
		case d.Mode == "":
			rendered, rerr = m.Render()
		case d.JSON:
			rendered, rerr = m.RenderForModeJSON(d.Mode)
		default:
			rendered, rerr = m.RenderForMode(d.Mode)
		}
		if rerr != nil {
			return fmt.Errorf("embedded %s.yaml failed render (mode=%q json=%v): %w", name, d.Mode, d.JSON, rerr)
		}
		if len(bytes.TrimSpace([]byte(rendered))) == 0 {
			return fmt.Errorf("embedded %s.yaml rendered empty (mode=%q json=%v)", name, d.Mode, d.JSON)
		}
		return nil
	}
	if b, err := fs.ReadFile(EmbeddedProfileFS(), name+".md"); err == nil {
		if len(bytes.TrimSpace(b)) == 0 {
			return fmt.Errorf("embedded %s.md is empty", name)
		}
		return nil
	}
	return fmt.Errorf("required profile %q not present in embedded supply (need %s.yaml or %s.md)", name, name, name)
}

// ValidateProfileClosure asserts at serve startup that every required
// profile resolves in every demanded mode through the real search-path
// lookup (user tiers + embedded). Unlike the embedded-only closure test,
// this also catches an invalid user override shadowing a valid embed.
// Callers should treat a non-nil return as event_type=profile_closure_violation.
func ValidateProfileClosure() error {
	var errs []error
	for name, demands := range RequiredProfiles {
		for _, d := range demands {
			var err error
			switch {
			case d.Mode == "" && d.JSON:
				_, _, err = loadProfileTemplateJSON(name)
			case d.Mode == "":
				_, _, err = loadProfileTemplate(name)
			case d.JSON:
				_, _, err = loadProfileTemplateForModeJSON(name, d.Mode)
			default:
				_, _, err = loadProfileTemplateForMode(name, d.Mode)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("profile %q (mode=%q json=%v): %w", name, d.Mode, d.JSON, err))
			}
		}
	}
	return errors.Join(errs...)
}
