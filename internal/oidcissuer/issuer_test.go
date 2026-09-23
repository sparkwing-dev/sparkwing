package oidcissuer_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/jwks"
	"github.com/sparkwing-dev/sparkwing/internal/oidcissuer"
)

func rsaPEM(t *testing.T, bits int, pkcs8 bool) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	if !pkcs8 {
		return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func runClaims() oidcissuer.Claims {
	return oidcissuer.Claims{
		Audience: "sts.amazonaws.com", Team: "acme", Pipeline: "deploy", Trigger: "webhook",
		RunnerKind: "runner", Ref: "refs/heads/main", SHA: "abc123", Repository: "github.com/acme/api", RunID: "run-1",
	}
}

// RFC 7638 section 3.1 publishes this key and its thumbprint, so the kid
// matches what any other implementation computes for the same key.
func TestThumbprintMatchesRFC7638Example(t *testing.T) {
	n, err := base64.RawURLEncoding.DecodeString("0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw")
	if err != nil {
		t.Fatal(err)
	}
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: 65537}
	if got, want := oidcissuer.Thumbprint(pub), "NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs"; got != want {
		t.Fatalf("thumbprint = %s, want %s", got, want)
	}
}

func TestSubjectFormatIsStable(t *testing.T) {
	sub, err := runClaims().Subject()
	if err != nil {
		t.Fatal(err)
	}
	if want := "team:acme:pipeline:deploy:trigger:webhook:runner:runner:ref:refs/heads/main"; sub != want {
		t.Fatalf("sub = %q, want %q", sub, want)
	}
	c := runClaims()
	c.Ref = ""
	if sub, err := c.Subject(); err != nil || !strings.HasSuffix(sub, ":ref:") {
		t.Fatalf("no ref: sub = %q, err = %v; want a subject ending in :ref:", sub, err)
	}
}

// A submitted value holding a separator could forge a later segment, and a
// wildcard would match as one inside a StringLike trust condition.
func TestSubjectRefusesValuesThatForgeSegments(t *testing.T) {
	for _, bad := range []struct{ field, value string }{
		{"pipeline", "deploy:trigger:webhook"},
		{"pipeline", "de*"},
		{"ref", "refs/heads/ma?n"},
		{"ref", "refs/heads/a b"},
		{"ref", "refs/heads/a\nb"},
		{"team", ""},
		{"pipeline", ""},
	} {
		c := runClaims()
		switch bad.field {
		case "pipeline":
			c.Pipeline = bad.value
		case "ref":
			c.Ref = bad.value
		case "team":
			c.Team = bad.value
		}
		if _, err := c.Subject(); !errors.Is(err, oidcissuer.ErrInvalidClaim) {
			t.Errorf("%s %q: err = %v, want ErrInvalidClaim", bad.field, bad.value, err)
		}
	}
}

func TestAudienceValidation(t *testing.T) {
	for _, ok := range []string{"sts.amazonaws.com", "api://AzureADTokenExchange", "//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/q", strings.Repeat("a", 256)} {
		if err := oidcissuer.ValidateAudience(ok); err != nil {
			t.Errorf("ValidateAudience(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", strings.Repeat("a", 257), "has space", "tab\t", "caf\xc3\xa9"} {
		if err := oidcissuer.ValidateAudience(bad); err == nil {
			t.Errorf("ValidateAudience(%q) = nil, want an error", bad)
		}
	}
}

func TestIssuerURLMustBeABareHTTPSOrigin(t *testing.T) {
	for _, ok := range []string{"https://api.sparkwing.dev", "http://127.0.0.1:8080", "http://localhost:9"} {
		if err := oidcissuer.ValidateIssuerURL(ok); err != nil {
			t.Errorf("ValidateIssuerURL(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "api.sparkwing.dev", "http://api.sparkwing.dev", "https://api.sparkwing.dev/", "https://api.sparkwing.dev/oidc", "https://api.sparkwing.dev?x=1", "https://u:p@api.sparkwing.dev"} {
		if err := oidcissuer.ValidateIssuerURL(bad); err == nil {
			t.Errorf("ValidateIssuerURL(%q) = nil, want an error", bad)
		}
	}
}

func TestNewRefusesWeakOrForeignKeysAndLifetimes(t *testing.T) {
	good := rsaPEM(t, 2048, true)
	if _, err := oidcissuer.New("https://api.sparkwing.dev", rsaPEM(t, 2048, false), nil, 0); err != nil {
		t.Errorf("PKCS #1 key: %v", err)
	}
	if _, err := oidcissuer.New("https://api.sparkwing.dev", rsaPEM(t, 1024, true), nil, 0); err == nil {
		t.Error("a 1024-bit key was accepted")
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oidcissuer.New("https://api.sparkwing.dev", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil, 0); err == nil {
		t.Error("an EC key was accepted for RS256")
	}
	if _, err := oidcissuer.New("https://api.sparkwing.dev", []byte("not pem"), nil, 0); err == nil {
		t.Error("a non-PEM key was accepted")
	}
	for _, ttl := range []time.Duration{30 * time.Second, 2 * time.Hour} {
		if _, err := oidcissuer.New("https://api.sparkwing.dev", good, nil, ttl); err == nil {
			t.Errorf("lifetime %s was accepted", ttl)
		}
	}
	if iss, err := oidcissuer.New("https://api.sparkwing.dev", good, nil, 0); err != nil || iss.TTL() != oidcissuer.DefaultTTL {
		t.Errorf("zero lifetime: iss = %v, err = %v; want the default", iss, err)
	}
}

// The previous key is published from either half of its PEM and never
// signs, and a previous key equal to the active one publishes once.
func TestPreviousKeyIsPublishedNotUsed(t *testing.T) {
	active := rsaPEM(t, 2048, true)
	prevKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&prevKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	prevPub := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	iss, err := oidcissuer.New("https://api.sparkwing.dev", active, prevPub, 0)
	if err != nil {
		t.Fatal(err)
	}
	ids := iss.KeyIDs()
	if len(ids) != 2 || ids[1] != oidcissuer.Thumbprint(&prevKey.PublicKey) {
		t.Fatalf("key ids = %v, want the active key then the previous", ids)
	}
	tok, _, err := iss.Mint(runClaims(), time.Now(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var head struct{ Kid string }
	if err := jwks.DecodeSegment(strings.Split(tok, ".")[0], &head); err != nil {
		t.Fatal(err)
	}
	if head.Kid != ids[0] {
		t.Fatalf("token kid = %s, want the active key %s", head.Kid, ids[0])
	}
	same, err := oidcissuer.New("https://api.sparkwing.dev", active, active, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(same.KeyIDs()); n != 1 {
		t.Fatalf("a previous key equal to the active one published %d keys, want 1", n)
	}
}

func TestMintCapsExpiryAtNotAfter(t *testing.T) {
	iss, err := oidcissuer.New("https://api.sparkwing.dev", rsaPEM(t, 2048, true), nil, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	_, exp, err := iss.Mint(runClaims(), now, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(now.Add(3 * time.Minute)) {
		t.Fatalf("exp = %s, want the credential's expiry %s", exp, now.Add(3*time.Minute))
	}
	_, exp, err = iss.Mint(runClaims(), now, time.Time{})
	if err != nil || !exp.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("exp = %s, err = %v; want issue time plus the lifetime", exp, err)
	}
	if _, _, err := iss.Mint(runClaims(), now, now.Add(-time.Second)); err == nil {
		t.Fatal("a credential already expired got a token")
	}
}

func TestDiscoveryCarriesTheFieldsAWSRequires(t *testing.T) {
	iss, err := oidcissuer.New("https://api.sparkwing.dev", rsaPEM(t, 2048, true), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(iss.Discovery())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["issuer"] != "https://api.sparkwing.dev" || doc["jwks_uri"] != "https://api.sparkwing.dev/.well-known/jwks.json" {
		t.Fatalf("issuer/jwks_uri = %v / %v", doc["issuer"], doc["jwks_uri"])
	}
	for field, want := range map[string]string{
		"response_types_supported":              "id_token",
		"subject_types_supported":               "public",
		"id_token_signing_alg_values_supported": "RS256",
		"claims_supported":                      "sub",
	} {
		list, _ := doc[field].([]any)
		found := false
		for _, v := range list {
			found = found || v == want
		}
		if !found {
			t.Errorf("%s = %v, want it to hold %q", field, doc[field], want)
		}
	}
}
