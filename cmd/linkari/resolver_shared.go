// resolver_shared.go — side-effect-free field resolution helper.
//
// resolveAllSecrets runs a simple flag-override pipeline for the secret
// fields. Pre-parse config ref expansion (expandConfigRefs) already
// resolves ${env:VAR}, ${secretsmanager:name#field}, and ${file:/path}
// before TOML decoding, so by the time ServerConfig reaches this function
// all values are plain strings. This function only applies flag overrides
// on top of what the loaded config already contains.
package main

import (
	"os"

	"github.com/blo-grindr/runabout/internal/secrets"
)

// SecretResolution holds the resolved result of a single secret field.
// Err is non-nil when the resolver pipeline returned an error (e.g. SM fetch
// failed). Value may be non-empty on success even when Err is nil — callers
// should check Err first.
type SecretResolution struct {
	Field string
	Value string
	Tier  string
	Src   secrets.Source
	Err   error
}

// resolveAllSecrets resolves token, firebase_sa, tsnet_authkey, jira_token,
// atlassian_email, atlassian_api_token, jira_domain, and pagerduty_token
// using flag-only overrides on top of the already-expanded ServerConfig values.
// Environment variables are checked as a fallback for any field that is still
// empty after TOML loading (covers the case where config.toml is absent).
//
// No AWS SDK calls are made — secrets were already resolved by expandConfigRefs
// at config parse time.
func resolveAllSecrets(cfg *ServerConfig) []SecretResolution {
	type fieldSpec struct {
		field  string
		env    string
		yamlFn func(*ServerConfig) string
	}
	specs := []fieldSpec{
		{
			field:  "token",
			env:    os.Getenv("LINKARI_TOKEN"),
			yamlFn: func(s *ServerConfig) string { return s.Token },
		},
		{
			field:  "firebase_sa",
			env:    os.Getenv("LINKARI_FIREBASE_SA"),
			yamlFn: func(s *ServerConfig) string { return s.FirebaseSA },
		},
		{
			field:  "tsnet_authkey",
			env:    os.Getenv("TS_AUTHKEY"),
			yamlFn: func(s *ServerConfig) string { return s.TSNetAuthKey },
		},
		{
			field:  "jira_token",
			env:    os.Getenv("LINKARI_JIRA_TOKEN"),
			yamlFn: func(s *ServerConfig) string { return s.JiraToken },
		},
		{
			field:  "atlassian_email",
			env:    os.Getenv("LINKARI_ATLASSIAN_EMAIL"),
			yamlFn: func(s *ServerConfig) string { return s.JiraAPIUsername },
		},
		{
			field:  "atlassian_api_token",
			env:    os.Getenv("LINKARI_ATLASSIAN_API_TOKEN"),
			yamlFn: func(s *ServerConfig) string { return s.JiraAPIPassword },
		},
		{
			field:  "jira_domain",
			env:    os.Getenv("LINKARI_JIRA_DOMAIN"),
			yamlFn: func(s *ServerConfig) string { return s.JiraDomain },
		},
		{
			field:  "pagerduty_token",
			env:    os.Getenv("LINKARI_PAGERDUTY_TOKEN"),
			yamlFn: func(s *ServerConfig) string { return s.PagerDutyToken },
		},
	}

	results := make([]SecretResolution, 0, len(specs))
	for _, spec := range specs {
		var value, tier string
		var yamlVal string
		if cfg != nil {
			yamlVal = spec.yamlFn(cfg)
		}
		switch {
		case yamlVal != "":
			value = yamlVal
			tier = "yaml-literal"
		case spec.env != "":
			value = spec.env
			tier = "env"
		default:
			tier = "default"
		}
		results = append(results, SecretResolution{
			Field: spec.field,
			Value: value,
			Tier:  tier,
			Src:   secrets.Source{Scheme: "literal", ID: "<literal>"},
		})
	}
	return results
}
