package protocol

import (
	"testing"
	"time"
)

func TestCanonicalJSONDeterminism(t *testing.T) {
	v := map[string]any{
		"z": "last",
		"a": "first",
		"n": 42,
		"b": true,
	}
	raw1, err := canonicalJSON(v)
	if err != nil {
		t.Fatalf("canonicalJSON error: %v", err)
	}
	raw2, err := canonicalJSON(v)
	if err != nil {
		t.Fatalf("canonicalJSON error: %v", err)
	}
	if string(raw1) != string(raw2) {
		t.Fatalf("canonicalJSON not deterministic: %q vs %q", raw1, raw2)
	}
	want := `{"a":"first","b":true,"n":42,"z":"last"}`
	if string(raw1) != want {
		t.Fatalf("canonicalJSON mismatch: got %q want %q", raw1, want)
	}
}

func TestNewRecordDeterministicHash(t *testing.T) {
	id := RecordID{ActorDID: "did:web:example.com", NSID: "com.openbooking.catalog.listing", RKey: "hotel-1"}
	value := map[string]any{"name": "Riverside", "currency": "USD"}
	r1, err := NewRecord(id, value, zeroTime())
	if err != nil {
		t.Fatalf("NewRecord: %v", err)
	}
	r2, err := NewRecord(id, value, zeroTime())
	if err != nil {
		t.Fatalf("NewRecord: %v", err)
	}
	if r1.Hash != r2.Hash {
		t.Fatalf("record hash not deterministic: %s vs %s", r1.Hash, r2.Hash)
	}
}

func zeroTime() time.Time {
	return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
}
