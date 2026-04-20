package did

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type Document struct {
	ID                 string               `json:"id"`
	VerificationMethod []VerificationMethod `json:"verificationMethod"`
	Service            []Service            `json:"service,omitempty"`
}

type VerificationMethod struct {
	ID                 string `json:"id"`
	Type               string `json:"type"`
	Controller         string `json:"controller"`
	PublicKeyMultibase string `json:"publicKeyMultibase,omitempty"`
	PublicKeyJWK       *JWK   `json:"publicKeyJwk,omitempty"`
	PublicKeyBase58    string `json:"publicKeyBase58,omitempty"`
}

type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
}

type Service struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	ServiceEndpoint any    `json:"serviceEndpoint"`
}

type Resolver interface {
	Resolve(ctx context.Context, did string) (Document, error)
}

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type WebResolver struct {
	Client HTTPClient
}

func (r WebResolver) Resolve(ctx context.Context, did string) (Document, error) {
	if !strings.HasPrefix(did, "did:web:") {
		return Document{}, fmt.Errorf("unsupported DID method: %s", did)
	}
	if r.Client == nil {
		r.Client = http.DefaultClient
	}
	target, err := didWebURL(did)
	if err != nil {
		return Document{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Document{}, err
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return Document{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Document{}, fmt.Errorf("resolve DID document: %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Document{}, err
	}
	var doc Document
	if err := json.Unmarshal(body, &doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}

func VerifyEd25519(doc Document, keyID string, message, signature []byte) error {
	publicKey, err := lookupEd25519PublicKey(doc, keyID)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, message, signature) {
		return fmt.Errorf("signature verification failed")
	}
	return nil
}

func HasVerificationMethod(doc Document, keyID string) error {
	_, err := lookupEd25519PublicKey(doc, keyID)
	return err
}

func didWebURL(did string) (string, error) {
	parts := strings.Split(did, ":")
	if len(parts) < 3 {
		return "", fmt.Errorf("invalid did:web")
	}
	hostPath := parts[2:]
	host := hostPath[0]
	path := "/.well-known/did.json"
	if len(hostPath) > 1 {
		path = "/" + strings.Join(hostPath[1:], "/") + "/did.json"
	}
	u := url.URL{
		Scheme: "https",
		Host:   host,
		Path:   path,
	}
	return u.String(), nil
}

func lookupEd25519PublicKey(doc Document, keyID string) (ed25519.PublicKey, error) {
	for _, method := range doc.VerificationMethod {
		if method.ID != keyID {
			continue
		}
		if method.PublicKeyJWK != nil {
			if method.PublicKeyJWK.Kty != "OKP" || method.PublicKeyJWK.Crv != "Ed25519" {
				return nil, fmt.Errorf("unsupported jwk key type")
			}
			raw, err := base64.RawURLEncoding.DecodeString(method.PublicKeyJWK.X)
			if err != nil {
				return nil, err
			}
			return ed25519.PublicKey(raw), nil
		}
		if method.PublicKeyMultibase != "" {
			return nil, fmt.Errorf("multibase keys are not yet implemented")
		}
		if method.PublicKeyBase58 != "" {
			return nil, fmt.Errorf("base58 keys are not yet implemented")
		}
	}
	return nil, fmt.Errorf("verification method not found: %s", keyID)
}

func ParsePKIXPublicKey(der []byte) (ed25519.PublicKey, error) {
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	publicKey, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is not Ed25519")
	}
	return publicKey, nil
}
