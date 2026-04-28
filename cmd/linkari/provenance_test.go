package main

import (
	"bytes"
	"log"
	"testing"

	"github.com/blo-grindr/runabout/internal/secrets"
)

// TestProvenanceLogFormat pins the canonical line format from EPIC-047
// locked decision #8:
//
//	linkari: secret <field> resolved from <source> fp=<8-hex> tier=<tier>
func TestProvenanceLogFormat(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer log.SetFlags(log.LstdFlags)

	entries := []provenanceEntry{
		{field: "token", source: "secretsmanager://linkari/bearer-token", fp: secrets.Fingerprint("hunter2"), tier: "yaml-sm"},
		{field: "tsnet_authkey", source: "<literal>", fp: secrets.Fingerprint("tskey-abc"), tier: "env"},
	}
	flushProvenance(entries)

	want := "linkari: secret token resolved from secretsmanager://linkari/bearer-token fp=" +
		secrets.Fingerprint("hunter2") + " tier=yaml-sm\n" +
		"linkari: secret tsnet_authkey resolved from <literal> fp=" +
		secrets.Fingerprint("tskey-abc") + " tier=env\n"

	if got := buf.String(); got != want {
		t.Errorf("provenance log mismatch\n got: %q\nwant: %q", got, want)
	}
}
