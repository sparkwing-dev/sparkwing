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

	"github.com/sparkwing-dev/sparkwing/internal/deployment"
)

func oidcTestKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func TestLoadOIDCIssuer(t *testing.T) {
	key := oidcTestKey(t)

	parse := func(t *testing.T, in deployment.Inputs) deployment.Config {
		t.Helper()
		cfg, err := deployment.Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	iss, err := loadOIDCIssuer(parse(t, deployment.Inputs{PublicOrigin: "https://api.sparkwing.dev"}), 0, &bytes.Buffer{})
	if err != nil || iss != nil {
		t.Fatalf("no key: issuer %v, err %v; want the feature off", iss, err)
	}
	if _, err := deployment.Parse(deployment.Inputs{OIDCPublishedKey: key, PublicOrigin: "https://api.sparkwing.dev"}); err == nil {
		t.Error("a published key with no signing key was accepted")
	}
	for _, external := range []string{"", "http://127.0.0.1:4344", "https://api.sparkwing.dev/oidc"} {
		if _, err := loadOIDCIssuer(parse(t, deployment.Inputs{OIDCKey: key, PublicOrigin: external}), 0, &bytes.Buffer{}); err == nil {
			t.Errorf("external URL %q was accepted as an issuer", external)
		}
	}

	var log bytes.Buffer
	creds, err := readCredentials(credentialsDir(t, map[string]string{credOIDCKey: string(key)}))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseDeployment(deploymentFlags{ExternalURL: "https://api.sparkwing.dev/"}, creds, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	iss, err = loadOIDCIssuer(cfg, 15*time.Minute, &log)
	if err != nil || iss == nil {
		t.Fatalf("credential key: issuer %v, err %v", iss, err)
	}
	if iss.URL() != "https://api.sparkwing.dev" || iss.TTL() != 15*time.Minute {
		t.Errorf("issuer %s ttl %s, want the trimmed external URL and 15m", iss.URL(), iss.TTL())
	}
	if strings.Contains(log.String(), "PRIVATE KEY") || !strings.Contains(log.String(), iss.KeyIDs()[0]) {
		t.Errorf("startup line %q must name the key id and hold no key material", log.String())
	}
}
