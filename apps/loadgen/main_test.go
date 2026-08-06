package main

import "testing"

func TestParseK6Output(t *testing.T) {
	output := "setup noise\nFLASHSEAT_RESULT={\"requests\":500,\"winners\":1,\"conflicts\":499,\"unexpectedErrors\":0,\"throughputRps\":9631.5,\"p50Ms\":34.3,\"p95Ms\":45.9,\"p99Ms\":49.6}\n"
	got, raw, err := parseK6Output(output)
	if err != nil {
		t.Fatal(err)
	}
	if got.Requests != 500 || got.Winners != 1 || got.Conflicts != 499 || got.P99MS != 49.6 {
		t.Fatalf("unexpected parsed summary: %+v", got)
	}
	if len(raw) == 0 {
		t.Fatal("raw summary was not retained")
	}
}

func TestParseK6OutputRejectsMissingMarker(t *testing.T) {
	if _, _, err := parseK6Output("not a result"); err == nil {
		t.Fatal("expected missing marker error")
	}
}
