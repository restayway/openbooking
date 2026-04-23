package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restayway/openbooking/pkg/did"
)

type mockResolver struct {
	doc did.Document
}

func (m *mockResolver) Resolve(ctx context.Context, did string) (did.Document, error) {
	return m.doc, nil
}

func encodeRawBase64(key ed25519.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(key)
}

func TestVerifyHTTPRequestSuccess(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	resolver := did.NewCachingResolver(
		&mockResolver{doc: did.Document{
			ID: "did:web:example.com",
			VerificationMethod: []did.VerificationMethod{{
				ID:         "did:web:example.com#sig",
				Type:       "JsonWebKey2020",
				Controller: "did:web:example.com",
				PublicKeyJWK: &did.JWK{
					Kty: "OKP", Crv: "Ed25519",
					X: encodeRawBase64(pub),
				},
			}},
		}},
		time.Hour, time.Hour,
	)

	verifier := NewVerifier(resolver, time.Minute)

	body := []byte(`{"name":"hotel"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/records", strings.NewReader(string(body)))
	if err := SignHTTPRequest(req, "did:web:example.com", MemorySigner{PrivateKey: priv}, body); err != nil {
		t.Fatalf("sign request: %v", err)
	}

	actorDID, err := verifier.VerifyHTTPRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("verify request: %v", err)
	}
	if actorDID != "did:web:example.com" {
		t.Fatalf("unexpected actor: %s", actorDID)
	}
}

func TestVerifyHTTPRequestMissingHeaders(t *testing.T) {
	verifier := NewVerifier(nil, time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/v1/records", nil)
	_, err := verifier.VerifyHTTPRequest(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for missing headers")
	}
}

func TestVerifyHTTPRequestTimestampSkew(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	resolver := did.NewCachingResolver(
		&mockResolver{doc: did.Document{
			ID: "did:web:example.com",
			VerificationMethod: []did.VerificationMethod{{
				ID:         "did:web:example.com#sig",
				Type:       "JsonWebKey2020",
				Controller: "did:web:example.com",
				PublicKeyJWK: &did.JWK{
					Kty: "OKP", Crv: "Ed25519",
					X: encodeRawBase64(pub),
				},
			}},
		}},
		time.Hour, time.Hour,
	)

	verifier := NewVerifier(resolver, time.Second)
	// Set clock to future so timestamp is stale
	verifier.clock = func() time.Time { return time.Now().Add(5 * time.Minute) }

	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/records", strings.NewReader(string(body)))
	_ = SignHTTPRequest(req, "did:web:example.com", MemorySigner{PrivateKey: priv}, body)

	_, err = verifier.VerifyHTTPRequest(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for timestamp skew")
	}
}

func TestMiddleware(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	resolver := did.NewCachingResolver(
		&mockResolver{doc: did.Document{
			ID: "did:web:example.com",
			VerificationMethod: []did.VerificationMethod{{
				ID:         "did:web:example.com#sig",
				Type:       "JsonWebKey2020",
				Controller: "did:web:example.com",
				PublicKeyJWK: &did.JWK{
					Kty: "OKP", Crv: "Ed25519",
					X: encodeRawBase64(pub),
				},
			}},
		}},
		time.Hour, time.Hour,
	)

	verifier := NewVerifier(resolver, time.Minute)

	handler := verifier.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := ActorDIDFromContext(r.Context())
		if actor != "did:web:example.com" {
			t.Fatalf("expected actor in context, got %s", actor)
		}
		w.WriteHeader(http.StatusOK)
	}))

	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/records", strings.NewReader(string(body)))
	if err := SignHTTPRequest(req, "did:web:example.com", MemorySigner{PrivateKey: priv}, body); err != nil {
		t.Fatalf("sign request: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
}
