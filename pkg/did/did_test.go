package did

import "testing"

func TestDidWebURL(t *testing.T) {
	got, err := didWebURL("did:web:example.com:user:alice")
	if err != nil {
		t.Fatalf("didWebURL: %v", err)
	}
	want := "https://example.com/user/alice/did.json"
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}
