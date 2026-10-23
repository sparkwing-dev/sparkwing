package engine

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func FuzzSignedPolicyRejectsDuplicateKeys(f *testing.F) {
	f.Add("field", uint8(0))
	f.Add("escaped\"\\\n", uint8(5))
	f.Add("", uint8(15))
	f.Fuzz(func(t *testing.T, name string, depth uint8) {
		key := testSigningKey(t)
		policy, now := policyFixture()
		body, err := json.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		field, err := json.Marshal(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, duplicate := range []bool{false, true} {
			extension := fmt.Sprintf(`{%s:1}`, field)
			if duplicate {
				extension = fmt.Sprintf(`{%s:1,%s:2}`, field, field)
			}
			for i := uint8(0); i < depth%16; i++ {
				if i%2 == 0 {
					extension = `{"nested":` + extension + `}`
				} else {
					extension = `[` + extension + `]`
				}
			}
			candidate := append(append([]byte(nil), body[:len(body)-1]...), []byte(`,"extension":`+extension+`}`)...)
			_, err := VerifyIndex(SignedIndex{Body: candidate, Signature: ed25519.Sign(key, candidate)}, now, signingKeys(key))
			if duplicate {
				requireCode(t, err, apierr.IntegrityFailure)
			} else if err != nil {
				t.Fatalf("unique extension refused: %v", err)
			}
		}
	})
}

func testSigningKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func policyFixture() (VersionIndex, time.Time) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	return VersionIndex{Format: 1, PublishedAt: now, Latest: "v0.20.0", Versions: []engineid.VersionInfo{
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
	index, err := VerifyIndex(signPolicy(t, key, policy), now, signingKeys(key))
	if err != nil {
		t.Fatal(err)
	}
	notices, err := Evaluate(index, "v0.10.2", now)
	if err != nil || len(notices) != 1 || notices[0].Kind != engineid.NoticeSupportEnded {
		t.Fatalf("%+v %v", notices, err)
	}
	restriction := Restriction{Version: "v0.10.2", Reason: "critical fixture vulnerability", Security: true, AnnouncedAt: now, EffectiveAt: now}
	policy.Revoked = []Restriction{restriction}
	index, err = VerifyIndex(signPolicy(t, key, policy), now, signingKeys(key))
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
	index, err = VerifyIndex(signPolicy(t, key, policy), now, signingKeys(key))
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
		_, err := VerifyIndex(SignedIndex{Body: body, Signature: ed25519.Sign(key, body)}, now, signingKeys(key))
		if err == nil {
			t.Fatal("invalid signed policy accepted")
		}
	}
	good.Signature = nil
	if _, err := VerifyIndex(good, now, signingKeys(key)); err == nil {
		t.Fatal("unsigned policy accepted")
	}
	if _, err := Evaluate(VerifiedIndex{}, "v0.10.2", now); err == nil {
		t.Fatal("zero authority accepted")
	}
	policy.Versions[1].SupportUntil = now
	if _, err := VerifyIndex(signPolicy(t, key, policy), now, signingKeys(key)); err == nil {
		t.Fatal("shortened support accepted")
	}
}

func signingKeys(key ed25519.PrivateKey) []ed25519.PublicKey {
	return []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}
}

func TestVerifyIndexUsesOnlySuppliedTrust(t *testing.T) {
	key := testSigningKey(t)
	policy, now := policyFixture()
	signed := signPolicy(t, key, policy)
	other := testSigningKey(t)
	for _, keys := range [][]ed25519.PublicKey{nil, {}, signingKeys(other)} {
		_, err := VerifyIndex(signed, now, keys)
		requireCode(t, err, apierr.IntegrityFailure)
	}
	if _, err := VerifyIndex(signed, now, signingKeys(key)); err != nil {
		t.Fatal(err)
	}
}
