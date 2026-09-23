package license_test

import (
	"crypto/ed25519"
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

func TestVerifyAcceptsALicenseWithSurroundingWhitespace(t *testing.T) {
	pub, priv := licensetest.NewKey(t)
	if _, err := license.Verify(" "+licensetest.Sign(t, priv, multiTeamTerms())+"\n", pub, now); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifyBoundsTheLicenseLength(t *testing.T) {
	const limit = 16 << 10
	pub, priv := licensetest.NewKey(t)
	var under, over string
	for pad := 12000; over == ""; pad++ {
		terms := multiTeamTerms()
		terms.IssuedTo = strings.Repeat("a", pad)
		raw := licensetest.Sign(t, priv, terms)
		if len(raw) <= limit {
			under = raw
		} else {
			over = raw
		}
	}
	if _, err := license.Verify(under, pub, now); err != nil {
		t.Fatalf("Verify(%d bytes) = %v, want accepted", len(under), err)
	}
	if _, err := license.Verify(over, pub, now); !errors.Is(err, license.ErrMalformed) {
		t.Fatalf("Verify(%d bytes) = %v, want ErrMalformed", len(over), err)
	}
}

func TestVerifyRefusesAKeyOfTheWrongSize(t *testing.T) {
	pub, priv := licensetest.NewKey(t)
	if _, err := license.Verify(licensetest.Sign(t, priv, multiTeamTerms()), pub[:16], now); err == nil {
		t.Fatal("Verify accepted a truncated public key")
	}
}

func TestVerifyRefusesJunkAfterASignedPayload(t *testing.T) {
	pub, priv := licensetest.NewKey(t)
	encPayload, encSig, _ := strings.Cut(licensetest.Sign(t, priv, multiTeamTerms()), ".")
	if _, err := license.Verify(encPayload+"!."+encSig, pub, now); !errors.Is(err, license.ErrMalformed) {
		t.Fatalf("Verify = %v, want ErrMalformed", err)
	}
}

func TestVerifyRefusesASignedPayloadItCannotRead(t *testing.T) {
	pub, priv := licensetest.NewKey(t)
	expires := now.Add(time.Hour).Format(time.RFC3339)
	for name, body := range map[string]string{
		"features not a list": `{"features":"multi-team","expires_at":"` + expires + `"}`,
		"no expiry":           `{"features":["multi-team"]}`,
	} {
		raw := licensetest.Encode([]byte(body), ed25519.Sign(priv, []byte(body)))
		if _, err := license.Verify(raw, pub, now); !errors.Is(err, license.ErrMalformed) {
			t.Errorf("%s: Verify = %v, want ErrMalformed", name, err)
		}
	}
}

func TestResolveGrantsAValidLicense(t *testing.T) {
	pub, priv := licensetest.NewKey(t)
	if lic := license.Resolve(licensetest.Sign(t, priv, multiTeamTerms()), pub, now, nil); !lic.Allows(license.FeatureMultiTeam, now) {
		t.Fatal("a valid multi-team license resolves to nothing")
	}
}

func TestVerifyGrantsOnlyTheListedFeatures(t *testing.T) {
	pub, priv := licensetest.NewKey(t)
	terms := multiTeamTerms()
	terms.Features = []string{"billing"}
	lic, err := license.Verify(licensetest.Sign(t, priv, terms), pub, now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if lic.Allows(license.FeatureMultiTeam, now) {
		t.Fatal("a license without multi-team allows multi-team")
	}
}
