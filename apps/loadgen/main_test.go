package main

import "testing"

func TestParseK6Output(t *testing.T) {
	output := "noise\nFLASHSEAT_RESULT={\"buyersArrived\":500,\"seatsPurchased\":812}\ntrailing log line\n"
	raw, err := parseK6Output(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"buyersArrived":500,"seatsPurchased":812}` {
		t.Fatalf("raw=%s", raw)
	}
	if _, err := parseK6Output("not a result"); err == nil {
		t.Fatal("expected missing marker error")
	}
}

func TestValidateAllowlist(t *testing.T) {
	ok := runRequest{RunID: "abc-123", EventID: "arena", Buyers: 100, ArrivalSeconds: 10, TargetURL: "http://gateway:8080/api/v1"}
	if err := ok.validate("http://gateway:8080/api/v1"); err != nil {
		t.Fatal(err)
	}
	bad := ok
	bad.TargetURL = "http://example.com"
	if bad.validate("http://gateway:8080/api/v1") == nil {
		t.Fatal("non-allowlisted target accepted")
	}
	bad = ok
	bad.RunID = "a;rm -rf"
	if bad.validate("http://gateway:8080/api/v1") == nil {
		t.Fatal("unsafe id accepted")
	}
}
