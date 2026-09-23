package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func oidcTestKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func TestLoadOIDCIssuer(t *testing.T) {
	key := oidcTestKey(t)
	t.Setenv(oidcKeyEnv, "")
	t.Setenv(oidcPreviousKeyEnv, "")

	iss, err := loadOIDCIssuer("", "", "https://api.sparkwing.dev", 0, &bytes.Buffer{})
	if err != nil || iss != nil {
		t.Fatalf("no key: issuer %v, err %v; want the feature off", iss, err)
	}
	if _, err := loadOIDCIssuer("", writeFile(t, "prev.pem", key), "https://api.sparkwing.dev", 0, &bytes.Buffer{}); err == nil {
		t.Error("a previous key with no current key was accepted")
	}
	for _, external := range []string{"", "http://api.sparkwing.dev", "https://api.sparkwing.dev/oidc"} {
		if _, err := loadOIDCIssuer(writeFile(t, "key.pem", key), "", external, 0, &bytes.Buffer{}); err == nil {
			t.Errorf("external URL %q was accepted as an issuer", external)
		}
	}

	var log bytes.Buffer
	iss, err = loadOIDCIssuer(writeFile(t, "key.pem", key), "", "https://api.sparkwing.dev/", 15*time.Minute, &log)
	if err != nil || iss == nil {
		t.Fatalf("file key: issuer %v, err %v", iss, err)
	}
	if iss.URL() != "https://api.sparkwing.dev" || iss.TTL() != 15*time.Minute {
		t.Errorf("issuer %s ttl %s, want the trimmed external URL and 15m", iss.URL(), iss.TTL())
	}
	if strings.Contains(log.String(), "PRIVATE KEY") || !strings.Contains(log.String(), iss.KeyIDs()[0]) {
		t.Errorf("startup line %q must name the key id and hold no key material", log.String())
	}

	t.Setenv(oidcKeyEnv, key)
	if iss, err := loadOIDCIssuer("", "", "https://api.sparkwing.dev", 0, &bytes.Buffer{}); err != nil || iss == nil {
		t.Fatalf("env key: issuer %v, err %v", iss, err)
	}
}
