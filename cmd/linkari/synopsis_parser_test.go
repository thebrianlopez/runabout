package main

import "testing"

func TestParseSynopsisFromEnvelopeAcceptsBareJSON(t *testing.T) {
	got, meta, err := parseSynopsisFromEnvelope([]byte(`{"synopsis":"  useful summary  "}`))
	if err != nil {
		t.Fatalf("parseSynopsisFromEnvelope: %v", err)
	}
	if got != "useful summary" {
		t.Fatalf("synopsis = %q, want trimmed bare JSON value", got)
	}
	if meta == nil {
		t.Fatal("metadata is nil, want empty metadata for bare JSON response")
	}
}

func TestParseSynopsisFromEnvelopeStillAcceptsCLIEnvelope(t *testing.T) {
	got, meta, err := parseSynopsisFromEnvelope([]byte(`{"type":"result","result":"{\"synopsis\":\"enveloped summary\"}","total_cost_usd":0.25}`))
	if err != nil {
		t.Fatalf("parseSynopsisFromEnvelope: %v", err)
	}
	if got != "enveloped summary" {
		t.Fatalf("synopsis = %q, want enveloped summary", got)
	}
	if meta == nil || meta.CostUSD != 0.25 {
		t.Fatalf("metadata = %+v, want cost 0.25", meta)
	}
}
