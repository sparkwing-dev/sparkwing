package engine

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/releaseauth"
)

func testSigningKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	old := releaseauth.TrustedPublicKeys
	releaseauth.TrustedPublicKeys = []string{base64.StdEncoding.EncodeToString(pub)}
	t.Cleanup(func() { releaseauth.TrustedPublicKeys = old })
	return key
}
func policyFixture() (VersionIndex, time.Time) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	return VersionIndex{Format: 1, PublishedAt: now, Latest: "v0.20.0", Versions: []VersionInfo{
		{Version: "v0.10.2", PublishedAt: now.AddDate(-2, 0, 0), SupportUntil: now.AddDate(-1, 0, 0)},
		{Version: "v0.20.0", PublishedAt: now.AddDate(0, -1, 0), SupportUntil: now.AddDate(0, 11, 0)},
	}}, now
}
func signPolicy(t *testing.T, key ed25519.PrivateKey, policy VersionIndex) SignedIndex {
	t.Helper()
	body, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	return SignedIndex{Body: body, Signature: ed25519.Sign(key, body)}
}
func requireCode(t *testing.T, err error, want apierr.Code) {
	t.Helper()
	var api *apierr.Error
	if !errors.As(err, &api) || api.Code != want {
		t.Fatalf("error=%v want code=%s", err, want)
	}
}
func TestSignedPolicyExpiryRevocationAndMinimum(t *testing.T) {
	key := testSigningKey(t)
	policy, now := policyFixture()
	index, err := VerifyIndex(signPolicy(t, key, policy), now)
	if err != nil {
		t.Fatal(err)
	}
	notices, err := Evaluate(index, "v0.10.2", now)
	if err != nil || len(notices) != 1 || notices[0].Kind != NoticeSupportEnded {
		t.Fatalf("%+v %v", notices, err)
	}
	restriction := Restriction{Version: "v0.10.2", Reason: "critical fixture vulnerability", Security: true, AnnouncedAt: now, EffectiveAt: now}
	policy.Revoked = []Restriction{restriction}
	index, err = VerifyIndex(signPolicy(t, key, policy), now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Evaluate(index, "v0.10.2", now)
	requireCode(t, err, apierr.VersionRevoked)
	var api *apierr.Error
	if !errors.As(err, &api) || api.Hint != UpgradeCommand {
		t.Fatalf("%v", err)
	}
	_, err = EvaluateRunner(index, "v0.10.2", now)
	if !errors.As(err, &api) || api.Hint == UpgradeCommand {
		t.Fatalf("runner hint: %v", err)
	}
	policy.Revoked = nil
	restriction.Version = "v0.20.0"
	policy.Minimum = restriction
	index, err = VerifyIndex(signPolicy(t, key, policy), now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Evaluate(index, "v0.10.2", now)
	requireCode(t, err, apierr.VersionUnsupported)
}
func TestSignedPolicyRejectsMalformedAndUnverifiedAuthority(t *testing.T) {
	key := testSigningKey(t)
	policy, now := policyFixture()
	good := signPolicy(t, key, policy)
	for _, body := range [][]byte{[]byte(`{"format":1,"format":1}`), []byte(`{"format":2}`), append(good.Body, []byte(` {}`)...)} {
		_, err := VerifyIndex(SignedIndex{Body: body, Signature: ed25519.Sign(key, body)}, now)
		if err == nil {
			t.Fatal("invalid signed policy accepted")
		}
	}
	good.Signature = nil
	if _, err := VerifyIndex(good, now); err == nil {
		t.Fatal("unsigned policy accepted")
	}
	if _, err := Evaluate(VerifiedIndex{}, "v0.10.2", now); err == nil {
		t.Fatal("zero authority accepted")
	}
	policy.Versions[1].SupportUntil = now
	if _, err := VerifyIndex(signPolicy(t, key, policy), now); err == nil {
		t.Fatal("shortened support accepted")
	}
}
