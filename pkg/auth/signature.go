package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/restayway/openbooking/pkg/did"
)

const (
	HeaderActorDID  = "X-OpenBooking-Actor-DID"
	HeaderSignature = "X-OpenBooking-Signature"
	HeaderTimestamp = "X-OpenBooking-Timestamp"
)

// Verifier validates HTTP request signatures against a DID resolver.
type Verifier struct {
	resolver did.Resolver
	clock    func() time.Time
	maxSkew  time.Duration
}

// NewVerifier creates a verifier with the given DID resolver.
// Requests with timestamps outside ±maxSkew from verifier clock are rejected.
func NewVerifier(resolver did.Resolver, maxSkew time.Duration) *Verifier {
	return &Verifier{
		resolver: resolver,
		clock:    time.Now,
		maxSkew:  maxSkew,
	}
}

// contextKey is used to store the authenticated DID in request context.
type contextKey struct{}

var actorDIDKey = contextKey{}

// ActorDIDFromContext returns the authenticated actor DID from the request context.
// If the request was not authenticated, it returns "".
func ActorDIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(actorDIDKey).(string); ok {
		return v
	}
	return ""
}

// VerifyHTTPRequest checks the signature headers on an incoming request.
// It returns the authenticated actor DID or an error.
func (v *Verifier) VerifyHTTPRequest(ctx context.Context, r *http.Request) (string, error) {
	actorDID := strings.TrimSpace(r.Header.Get(HeaderActorDID))
	sigB64 := strings.TrimSpace(r.Header.Get(HeaderSignature))
	tsStr := strings.TrimSpace(r.Header.Get(HeaderTimestamp))

	if actorDID == "" {
		return "", fmt.Errorf("missing %s header", HeaderActorDID)
	}
	if sigB64 == "" {
		return "", fmt.Errorf("missing %s header", HeaderSignature)
	}
	if tsStr == "" {
		return "", fmt.Errorf("missing %s header", HeaderTimestamp)
	}

	ts, err := time.Parse(time.RFC3339, tsStr)
	if err != nil {
		return "", fmt.Errorf("invalid timestamp format: %w", err)
	}
	now := v.clock()
	if ts.Before(now.Add(-v.maxSkew)) || ts.After(now.Add(v.maxSkew)) {
		return "", fmt.Errorf("timestamp skew too large")
	}

	body, err := readBody(r)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	payload := signingPayload(r.Method, r.URL.Path, tsStr, body)

	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return "", fmt.Errorf("invalid signature encoding: %w", err)
	}

	doc, err := v.resolver.Resolve(ctx, actorDID)
	if err != nil {
		return "", fmt.Errorf("resolve did: %w", err)
	}

	// Determine key ID from header or default to actor DID + #sig
	keyID := actorDID + "#sig"
	if strings.Contains(sigB64, ":") {
		// Not applicable for base64; kept for future key-id-in-header extension
	}

	if err := did.VerifyEd25519(doc, keyID, payload, sig); err != nil {
		return "", fmt.Errorf("signature verification failed: %w", err)
	}

	return actorDID, nil
}

// Middleware returns an HTTP middleware that enforces request signatures.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actorDID, err := v.VerifyHTTPRequest(r.Context(), r)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		ctx := context.WithValue(r.Context(), actorDIDKey, actorDID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// signingPayload creates the deterministic byte slice that must be signed.
func signingPayload(method, path, timestamp string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(fmt.Sprintf("%s\n%s\n%s\n%s", strings.ToUpper(method), path, timestamp, hex.EncodeToString(sum[:])))
}

func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	// Reset body so subsequent reads / json.Decoder still work.
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

// SignHTTPRequest signs an HTTP request using the provided signer and DID.
// It mutates the request to add the required signature headers.
func SignHTTPRequest(r *http.Request, actorDID string, signer Signer, body []byte) error {
	ts := time.Now().UTC().Format(time.RFC3339)
	payload := signingPayload(r.Method, r.URL.Path, ts, body)
	sig, err := signer.Sign(payload)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	r.Header.Set(HeaderActorDID, actorDID)
	r.Header.Set(HeaderTimestamp, ts)
	r.Header.Set(HeaderSignature, base64.StdEncoding.EncodeToString(sig))
	return nil
}

// Signer is the interface required to sign request payloads.
type Signer interface {
	Sign(payload []byte) ([]byte, error)
}

// MemorySigner implements Signer for testing.
type MemorySigner struct {
	PrivateKey ed25519.PrivateKey
}

func (m MemorySigner) Sign(payload []byte) ([]byte, error) {
	return ed25519.Sign(m.PrivateKey, payload), nil
}
