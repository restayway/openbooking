package protocol

import (
	"testing"
)

func TestEncodeSHA256CIDDeterministicAndValid(t *testing.T) {
	input := [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}

	c1, err := EncodeSHA256CID(input)
	if err != nil {
		t.Fatalf("encode error: %v", err)
	}
	c2, err := EncodeSHA256CID(input)
	if err != nil {
		t.Fatalf("encode error: %v", err)
	}
	if c1 != c2 {
		t.Fatalf("CID not deterministic: %q vs %q", c1, c2)
	}

	// Must be a valid multibase string starting with 'b' (base32 lower)
	if len(c1) == 0 || c1[0] != 'b' {
		t.Fatalf("expected base32lower multibase CID starting with 'b', got %q", c1)
	}
}

func TestCanonicalCIDDeterminism(t *testing.T) {
	v := map[string]any{"name": "Hotel", "stars": 5}
	c1, err := CanonicalHash(v)
	if err != nil {
		t.Fatalf("canonical cid: %v", err)
	}
	c2, err := CanonicalHash(v)
	if err != nil {
		t.Fatalf("canonical cid: %v", err)
	}
	if c1 != c2 {
		t.Fatalf("canonical CID not deterministic: %q vs %q", c1, c2)
	}
}
