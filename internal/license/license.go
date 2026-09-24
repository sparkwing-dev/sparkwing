// Package license verifies the signed license that unlocks features a
// controller does not offer by default, such as hosting more than one team.
//
// A license is one line of text:
//
//	base64url(payload) "." base64url(signature)
//
// where payload is the JSON document
//
//	{"features":["multi-team"],"issued_to":"...","issued_at":"<RFC 3339>","expires_at":"<RFC 3339>"}
//
// and signature is the Ed25519 signature of exactly those payload bytes.
// Both halves use unpadded URL-safe base64. The controller checks the
// signature against the public key embedded in this package, so a license
// cannot be written by anyone who does not hold the matching private key,
// which lives outside this repository.
package license

import (
	"crypto/ed25519"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
)

// FeatureMultiTeam lets a deployment hold more than one team.
const FeatureMultiTeam = "multi-team"

// FeatureMetering lets a deployment meter runner and storage usage. Existing
// multi-team licenses grant it too, so they remain valid without re-issuance.
const FeatureMetering = "metering"

//go:embed license_key.pub
var embeddedKey string

// Errors Verify returns. Each one leaves the deployment unlicensed.
var (
	ErrMalformed    = errors.New("license: malformed")
	ErrBadSignature = errors.New("license: signature does not verify")
	ErrExpired      = errors.New("license: expired")
)

// safety: a license is a few hundred bytes and can arrive through an environment variable, so decoding is bounded.
const maxLicenseLen = 16 << 10

type payload struct {
	Features  []string  `json:"features"`
	IssuedTo  string    `json:"issued_to"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// License is a verified license. Its fields are unexported so that the only
// way to hold one is through Verify, which checked the signature.
type License struct {
	features  []string
	issuedTo  string
	issuedAt  time.Time
	expiresAt time.Time
}

// IssuedTo names who the license was issued to.
func (l *License) IssuedTo() string { return l.issuedTo }

// ExpiresAt is when the license stops granting anything.
func (l *License) ExpiresAt() time.Time { return l.expiresAt }

// Allows reports whether the license grants feature at now. A nil license
// grants nothing, so a caller holding the result of a failed Resolve asks the
// same question and gets the unlicensed answer.
func (l *License) Allows(feature string, now time.Time) bool {
	if l == nil || !now.Before(l.expiresAt) {
		return false
	}
	return slices.Contains(l.features, feature) ||
		(feature == FeatureMetering && slices.Contains(l.features, FeatureMultiTeam))
}

// EmbeddedKey returns the public key this build verifies licenses against.
func EmbeddedKey() (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(embeddedKey))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("license: embedded public key is unusable")
	}
	return ed25519.PublicKey(raw), nil
}

// Verify checks raw against key and returns the license it carries. It
// refuses a license whose signature does not verify, whose payload does not
// parse, or that has expired at now.
func Verify(raw string, key ed25519.PublicKey, now time.Time) (*License, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxLicenseLen {
		return nil, ErrMalformed
	}
	if len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("license: public key is %d bytes, want %d", len(key), ed25519.PublicKeySize)
	}
	encPayload, encSig, ok := strings.Cut(raw, ".")
	if !ok {
		return nil, ErrMalformed
	}
	body, err := base64.RawURLEncoding.DecodeString(encPayload)
	if err != nil {
		return nil, ErrMalformed
	}
	sig, err := base64.RawURLEncoding.DecodeString(encSig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, ErrMalformed
	}
	// safety: the signature is checked before the payload is parsed, so an
	// unsigned document never reaches the JSON decoder.
	if !ed25519.Verify(key, body, sig) {
		return nil, ErrBadSignature
	}
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, ErrMalformed
	}
	if p.ExpiresAt.IsZero() {
		return nil, ErrMalformed
	}
	if !now.Before(p.ExpiresAt) {
		return nil, ErrExpired
	}
	return &License{
		features:  slices.Clone(p.Features),
		issuedTo:  p.IssuedTo,
		issuedAt:  p.IssuedAt,
		expiresAt: p.ExpiresAt,
	}, nil
}

// Resolve verifies raw against key and logs the outcome once. It never fails:
// an absent or unusable license returns nil, which grants nothing, because a
// local install runs without a license and must keep starting.
func Resolve(raw string, key ed25519.PublicKey, now time.Time, logger *slog.Logger) *License {
	if logger == nil {
		logger = slog.Default()
	}
	if strings.TrimSpace(raw) == "" {
		logger.Info("no license configured; this controller holds one team")
		return nil
	}
	lic, err := Verify(raw, key, now)
	if err != nil {
		logger.Warn("license refused; this controller holds one team", "reason", err.Error())
		return nil
	}
	logger.Info("license accepted",
		"issued_to", lic.issuedTo, "features", lic.features, "expires_at", lic.expiresAt.Format(time.RFC3339))
	return lic
}
