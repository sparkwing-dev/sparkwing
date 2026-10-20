package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
)

func signFlowState(key []byte, v any) (string, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(mac.Sum(nil)), nil
}

func openFlowState(key []byte, raw string, v any) bool {
	enc := base64.RawURLEncoding
	body, sig, ok := strings.Cut(raw, ".")
	if !ok {
		return false
	}
	payload, err := enc.DecodeString(body)
	if err != nil {
		return false
	}
	got, err := enc.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return false
	}
	return json.Unmarshal(payload, v) == nil
}

// safety: Flow state carries the verifier digest, never the browser's PKCE verifier.
func verifierDigest(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
