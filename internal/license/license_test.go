package license_test

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
)

var now = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

func multiTeamTerms() licensetest.Terms {
	return licensetest.Terms{
		Features:  []string{license.FeatureMultiTeam},
		IssuedTo:  "acme",
		IssuedAt:  now.Add(-time.Hour),
		ExpiresAt: now.Add(24 * time.Hour),
	}
}

func TestVerifyAcceptsALicenseSignedByTheKey(t *testing.T) {
	pub, priv := licensetest.NewKey(t)
	lic, err := license.Verify(licensetest.Sign(t, priv, multiTeamTerms()), pub, now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !lic.Allows(license.FeatureMultiTeam, now) {
		t.Fatal("a valid multi-team license does not allow multi-team")
	}
	if lic.Allows("billing", now) {
		t.Fatal("a license allows a feature it does not list")
	}
	if lic.Allows(license.FeatureMultiTeam, now.Add(25*time.Hour)) {
		t.Fatal("a license still allows its feature after it expired")
	}
}

func TestVerifyRefusesALicenseSignedByAnotherKey(t *testing.T) {
	pub, _ := licensetest.NewKey(t)
	_, otherPriv := licensetest.NewKey(t)
	_, err := license.Verify(licensetest.Sign(t, otherPriv, multiTeamTerms()), pub, now)
	if !errors.Is(err, license.ErrBadSignature) {
		t.Fatalf("Verify = %v, want ErrBadSignature", err)
	}
}

func TestVerifyRefusesATamperedPayload(t *testing.T) {
	pub, priv := licensetest.NewKey(t)
	signed := licensetest.Sign(t, priv, licensetest.Terms{
		Features: []string{"billing"}, IssuedTo: "acme",
		IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour),
	})
	encPayload, encSig, _ := strings.Cut(signed, ".")
	body, err := base64.RawURLEncoding.DecodeString(encPayload)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(encSig)
	if err != nil {
		t.Fatal(err)
	}
	tampered := []byte(strings.Replace(string(body), `"billing"`, `"multi-team"`, 1))
	_, err = license.Verify(licensetest.Encode(tampered, sig), pub, now)
	if !errors.Is(err, license.ErrBadSignature) {
		t.Fatalf("Verify = %v, want ErrBadSignature", err)
	}
}

func TestVerifyRefusesAnExpiredLicense(t *testing.T) {
	pub, priv := licensetest.NewKey(t)
	terms := multiTeamTerms()
	terms.ExpiresAt = now.Add(-time.Minute)
	_, err := license.Verify(licensetest.Sign(t, priv, terms), pub, now)
	if !errors.Is(err, license.ErrExpired) {
		t.Fatalf("Verify = %v, want ErrExpired", err)
	}
}

func TestVerifyRefusesMalformedText(t *testing.T) {
	pub, _ := licensetest.NewKey(t)
	for _, raw := range []string{"", "no-dot", "!!!.!!!", "YQ.YQ"} {
		if _, err := license.Verify(raw, pub, now); !errors.Is(err, license.ErrMalformed) {
			t.Errorf("Verify(%q) = %v, want ErrMalformed", raw, err)
		}
	}
}

func TestResolveGrantsNothingWithoutAUsableLicense(t *testing.T) {
	pub, _ := licensetest.NewKey(t)
	if lic := license.Resolve("", pub, now, nil); lic.Allows(license.FeatureMultiTeam, now) {
		t.Fatal("an absent license allows multi-team")
	}
	if lic := license.Resolve("garbage", pub, now, nil); lic.Allows(license.FeatureMultiTeam, now) {
		t.Fatal("a malformed license allows multi-team")
	}
}

func TestEmbeddedKeyIsUsable(t *testing.T) {
	if _, err := license.EmbeddedKey(); err != nil {
		t.Fatal(err)
	}
}
