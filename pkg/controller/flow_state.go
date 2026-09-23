package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
)

// signFlowState encodes v and signs it with key, for a state an OAuth flow
// carries out through the provider and back.
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

// openFlowState decodes raw into v and reports whether key signed it. v is
// meaningless when it reports false.
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

// verifierDigest is what a flow state carries in place of its PKCE verifier,
// so the state proves which browser holds the verifier without revealing it.
func verifierDigest(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
