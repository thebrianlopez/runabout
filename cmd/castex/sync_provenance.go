package main

import (
	"bytes"
	"encoding/json"
)

// provenanceField is the reserved envelope key carrying sync provenance on an
// event line. The underscore prefix marks it as transport metadata rather than
// a schema field, so event-schema registration does not need to claim it.
//
// This key is authoritative-on-ingest: whatever value a remote object already
// carries under this key is discarded and replaced at download time. A remote
// peer therefore cannot forge a "local" provenance tag by embedding one in the
// object it publishes.
const provenanceField = "_castex_provenance"

// ProvenanceSource identifies how an event entered the local store.
type ProvenanceSource string

const (
	// ProvenanceLocal marks an event produced on this machine. Locally produced
	// events are never rewritten, so they carry no provenance envelope at all;
	// absence of the envelope is what means "local".
	ProvenanceLocal ProvenanceSource = "local"
	// ProvenanceRemote marks an event ingested from a sync remote.
	ProvenanceRemote ProvenanceSource = "remote"
)

// ProvenanceInfo describes the origin of a downloaded object. It is stamped
// onto every event line ingested from a remote so that downloaded events are
// never indistinguishable from locally produced ones.
type ProvenanceInfo struct {
	Source    ProvenanceSource `json:"source"`
	Remote    string           `json:"remote,omitempty"`     // remote endpoint, e.g. s3://bucket/prefix
	Peer      string           `json:"peer,omitempty"`       // peer identity, when the transport supplies one
	ObjectKey string           `json:"object_key,omitempty"` // remote object key this line came from
	ETag      string           `json:"etag,omitempty"`       // remote-reported content tag, when available
	SyncedAt  string           `json:"synced_at,omitempty"`  // UTC ingest timestamp
}

// ProvenanceDecision is the verdict returned by a ProvenanceFilter.
type ProvenanceDecision struct {
	Allow  bool
	Reason string
}

// ProvenanceFilter decides whether an object from a given origin may be
// ingested. It is consulted once per remote object, before the object is
// fetched, so a rejecting policy costs no transfer.
//
// This is the extension point for future trust policy (peer allowlists,
// signature verification, endpoint pinning). It exists now, permissive, so
// that adding such a policy is a filter implementation rather than another
// on-disk migration.
type ProvenanceFilter func(ProvenanceInfo) ProvenanceDecision

// AllowAllProvenance is the default filter. It accepts every origin.
//
// This default is deliberately permissive: this change introduces the
// provenance seam, not a trust policy. Tightening the default is a separate,
// explicit decision - it must not happen by accident.
func AllowAllProvenance(ProvenanceInfo) ProvenanceDecision {
	return ProvenanceDecision{Allow: true, Reason: "default_permissive"}
}

// resolveProvenanceFilter returns the configured filter, or the permissive
// default when none is set. It never returns nil, so callers may invoke the
// result unconditionally.
func resolveProvenanceFilter(f ProvenanceFilter) ProvenanceFilter {
	if f == nil {
		return AllowAllProvenance
	}
	return f
}

// tagEventLines stamps info onto every JSON-object line in data and returns the
// rewritten JSONL payload, the number of lines tagged, and the number dropped.
//
// A line is droppable only if it is not a JSON object. Such lines are already
// ignored by every reader in this package (readSyncEvents and parseRemoteEvents
// both skip them), so dropping them here removes content no consumer indexes
// while guaranteeing the invariant that matters: every line written by the
// download path carries a provenance envelope.
func tagEventLines(data []byte, info ProvenanceInfo) (tagged []byte, kept int, dropped int) {
	stamp, err := json.Marshal(info)
	if err != nil {
		return nil, 0, 0
	}

	var out bytes.Buffer
	for _, line := range splitLines(data) {
		// splitLines does not drop blanks despite its doc comment; skip them
		// silently, as readSyncEvents and parseRemoteEvents both do, so a
		// trailing newline never counts as dropped content.
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(line, &m); err != nil || m == nil {
			dropped++
			continue
		}
		// Overwrite unconditionally - never trust a remote-supplied envelope.
		m[provenanceField] = json.RawMessage(stamp)
		enc, err := json.Marshal(m)
		if err != nil {
			dropped++
			continue
		}
		out.Write(enc)
		out.WriteByte('\n')
		kept++
	}
	return out.Bytes(), kept, dropped
}

// provenanceOf reports the recorded origin of a single event line.
//
// An absent envelope means locally produced. A present but unreadable envelope
// is treated as remote, not local: a malformed tag is a reason for suspicion,
// not for granting local trust.
func provenanceOf(raw []byte) ProvenanceInfo {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return ProvenanceInfo{Source: ProvenanceLocal}
	}
	v, ok := m[provenanceField]
	if !ok {
		return ProvenanceInfo{Source: ProvenanceLocal}
	}
	var info ProvenanceInfo
	if err := json.Unmarshal(v, &info); err != nil || info.Source == "" {
		return ProvenanceInfo{Source: ProvenanceRemote}
	}
	return info
}

// canonicalEventContent returns a key-sorted re-encoding of raw with the
// provenance envelope removed, so two lines can be compared on payload alone.
func canonicalEventContent(raw []byte) ([]byte, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, false
	}
	delete(m, provenanceField)
	enc, err := json.Marshal(m)
	if err != nil {
		return nil, false
	}
	return enc, true
}

// sameEventContent reports whether two event lines carry the same payload,
// ignoring provenance envelopes and key ordering.
//
// Provenance-blind comparison is required for correctness, not just tidiness:
// tagging rewrites a downloaded line, so a byte-exact comparison would flag
// every previously downloaded event as a fresh conflict on the next sync.
func sameEventContent(a, b []byte) bool {
	ca, aok := canonicalEventContent(a)
	cb, bok := canonicalEventContent(b)
	if !aok || !bok {
		return bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b))
	}
	return bytes.Equal(ca, cb)
}
