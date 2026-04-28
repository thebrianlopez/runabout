package main

import (
	"context"
	"testing"

	"github.com/blo-grindr/runabout/internal/secrets"
)

type fakeSM map[string]string

func (f fakeSM) GetSecretValue(_ context.Context, id string) (string, error) {
	return f["ok"], nil
}

func TestResolveServerField(t *testing.T) {
	r := secrets.New(func(_ context.Context) (secrets.SecretsManagerAPI, error) {
		return fakeSM{"ok": "from-sm"}, nil
	})
	ctx := context.Background()

	cases := []struct {
		name                          string
		flag, env, yaml, def          string
		wantValue, wantTier, wantSrc  string
	}{
		{"flag wins", "F", "E", "Y", "D", "F", "flag", "literal"},
		{"env wins", "", "E", "Y", "D", "E", "env", "literal"},
		{"yaml literal", "", "", "Y", "D", "Y", "yaml-literal", "literal"},
		{"yaml sm", "", "", "secretsmanager://x", "D", "from-sm", "yaml-sm", "secretsmanager"},
		{"default", "", "", "", "D", "D", "default", "literal"},
		{"all empty", "", "", "", "", "", "default", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, tier, src, err := resolveServerField(ctx, r, tc.flag, tc.env, tc.yaml, tc.def)
			if err != nil {
				t.Fatal(err)
			}
			if v != tc.wantValue {
				t.Errorf("value=%q want %q", v, tc.wantValue)
			}
			if tier != tc.wantTier {
				t.Errorf("tier=%q want %q", tier, tc.wantTier)
			}
			if src.Scheme != tc.wantSrc {
				t.Errorf("src.Scheme=%q want %q", src.Scheme, tc.wantSrc)
			}
		})
	}
}
