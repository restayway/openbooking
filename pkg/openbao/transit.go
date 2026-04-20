package openbao

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type TransitClient struct {
	Address string
	Token   string
	Client  *http.Client
}

func (c TransitClient) Preflight(ctx context.Context, keyID string) error {
	_, err := c.Sign(ctx, []byte("openbooking-preflight"), keyID)
	return err
}

func (c TransitClient) Sign(ctx context.Context, message []byte, keyID string) ([]byte, error) {
	return c.signOrVerify(ctx, message, keyID, "")
}

func (c TransitClient) Verify(ctx context.Context, message []byte, keyID string, signature []byte) error {
	sig := "vault:v1:" + base64.StdEncoding.EncodeToString(signature)
	_, err := c.signOrVerify(ctx, message, keyID, sig)
	return err
}

func (c TransitClient) Encrypt(ctx context.Context, plaintext []byte, keyID string) (string, error) {
	if c.Client == nil {
		c.Client = http.DefaultClient
	}
	payload := map[string]string{
		"plaintext": base64.StdEncoding.EncodeToString(plaintext),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Address, "/")+"/v1/transit/encrypt/"+keyID, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vault-Token", c.Token)

	resp, err := c.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("transit encrypt failed: %s", resp.Status)
	}
	var decoded struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", err
	}
	return decoded.Data.Ciphertext, nil
}

func (c TransitClient) Decrypt(ctx context.Context, ciphertext, keyID string) ([]byte, error) {
	if c.Client == nil {
		c.Client = http.DefaultClient
	}
	payload := map[string]string{
		"ciphertext": ciphertext,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Address, "/")+"/v1/transit/decrypt/"+keyID, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vault-Token", c.Token)

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("transit decrypt failed: %s", resp.Status)
	}
	var decoded struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(decoded.Data.Plaintext)
}

func (c TransitClient) signOrVerify(ctx context.Context, message []byte, keyID, signature string) ([]byte, error) {
	if c.Client == nil {
		c.Client = http.DefaultClient
	}
	payload := map[string]string{
		"input": base64.StdEncoding.EncodeToString(message),
	}
	action := "sign"
	if signature != "" {
		action = "verify"
		payload["signature"] = signature
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Address, "/")+"/v1/transit/"+action+"/"+keyID, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vault-Token", c.Token)

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("transit request failed: %s", resp.Status)
	}
	var decoded struct {
		Data struct {
			Signature string `json:"signature"`
			Valid     bool   `json:"valid"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	if signature != "" {
		if !decoded.Data.Valid {
			return nil, fmt.Errorf("signature rejected by transit verifier")
		}
		return nil, nil
	}
	trimmed := strings.TrimPrefix(decoded.Data.Signature, "vault:v1:")
	return base64.StdEncoding.DecodeString(trimmed)
}
