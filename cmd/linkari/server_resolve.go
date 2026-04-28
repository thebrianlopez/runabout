// EPIC-047 M3/M4: ServerConfig field resolution pipeline.
//
// resolveServerField applies the documented resolution order:
//
//	flag > env > yaml > default
//
// and routes the chosen value through secrets.Resolver so secretsmanager://
// and file:// URIs are dereferenced. Returns the resolved value plus a
// (tier, source) tuple suitable for emitting a startup provenance log line.
//
// Tiers:
//
//	flag         — non-empty CLI flag value won
//	env          — non-empty env var value won
//	yaml-literal — yaml value won and was a plain literal
//	yaml-sm      — yaml value won and was a secretsmanager:// URI
//	yaml-file    — yaml value won and was a file:// URI
//	default      — every layer was empty; built-in default returned
//
// An empty default with all layers empty returns ("", "default", Source{}, nil).
// Per the locked decision (#4), Resolve("") is short-circuited — no provenance
// line should be emitted by callers when tier=="default" and value is empty.
package main

import (
	"context"
	"strings"

	"github.com/blo-grindr/runabout/internal/secrets"
)

// resolveServerField picks the highest-precedence non-empty value from
// (flag, env, yamlVal, def) and runs it through r.Resolve. The chosen layer
// determines the tier label. The Source returned by Resolve is preserved.
func resolveServerField(
	ctx context.Context,
	r *secrets.Resolver,
	flag, env, yamlVal, def string,
) (value string, tier string, src secrets.Source, err error) {
	var raw string
	switch {
	case flag != "":
		raw, tier = flag, "flag"
	case env != "":
		raw, tier = env, "env"
	case yamlVal != "":
		raw = yamlVal
		switch {
		case strings.HasPrefix(yamlVal, "secretsmanager://"):
			tier = "yaml-sm"
		case strings.HasPrefix(yamlVal, "file://"):
			tier = "yaml-file"
		default:
			tier = "yaml-literal"
		}
	default:
		raw, tier = def, "default"
	}

	// Locked decision #4: empty short-circuit. Skip Resolve entirely.
	if raw == "" {
		return "", tier, secrets.Source{}, nil
	}

	value, src, err = r.Resolve(ctx, raw)
	return value, tier, src, err
}
