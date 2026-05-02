// resolver_shared.go — side-effect-free secret resolution helper.
//
// resolveAllSecrets runs the EPIC-047 resolver pipeline for the three secret
// fields (token, firebase_sa, tsnet_authkey) without any side effects: no
// materialization, no file writes, no listener binding. Used by both
// `linkari doctor` (direct consumer) and available for future callers that
// need resolved values before any server bring-up.
package main

import (
	"context"
	"os"

	"github.com/thebrianlopez/runabout/cmd/linkari/internal/secrets"
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

// resolveAllSecrets resolves token, firebase_sa, and tsnet_authkey through
// the EPIC-047 resolver pipeline. Resolution order for each field:
//
//	env > server.yaml literal > server.yaml secretsmanager:// URI > (no default)
//
// There are no CLI-flag inputs here — doctor has no serve flags. Callers that
// need flag-layer resolution (i.e. serveCmd) should keep using resolveServerField
// directly within RunE.
//
// No side effects: firebase_sa JSON content is NOT materialized to cache.
// The returned Value for firebase_sa will be the raw secret value (JSON string)
// if sourced from SM, or a file path if sourced from env/literal/file://.
func resolveAllSecrets(ctx context.Context, r *secrets.Resolver, cfg *ServerConfig) []SecretResolution {
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
	}

	results := make([]SecretResolution, 0, len(specs))
	for _, spec := range specs {
		var yamlVal string
		if cfg != nil {
			yamlVal = spec.yamlFn(cfg)
		}
		value, tier, src, err := resolveServerField(ctx, r, "", spec.env, yamlVal, "")
		results = append(results, SecretResolution{
			Field: spec.field,
			Value: value,
			Tier:  tier,
			Src:   src,
			Err:   err,
		})
	}
	return results
}
