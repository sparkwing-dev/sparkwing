package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func TestConfigureGitHubApp(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	full := githubAppFlags{AppID: "4242", Slug: "sparkwing", ClientID: "Iv1.x", ClientSecret: "s", PrivateKey: pemText}
	hook, err := deployment.NewKeyPair("hook", "hook.previous", []byte("whsec"), nil)
	if err != nil {
		t.Fatal(err)
	}

	configureGitHubApp(controller.New(nil, nil), githubAppFlags{}, deployment.KeyPair{})
	srv := controller.New(nil, nil)
	configureGitHubApp(srv, full, hook)
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/webhooks/github-app", strings.NewReader(`{}`)))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("configured App accepted an unsigned delivery: %d %s", response.Code, response.Body)
	}
	previous, err := deployment.Parse(deployment.Inputs{PreviousWebhookSecret: []byte("previous")})
	if err != nil {
		t.Fatal(err)
	}
	without := func(edit func(*githubAppFlags)) githubAppFlags {
		f := full
		edit(&f)
		return f
	}
	cases := map[string]struct {
		flags githubAppFlags
		hook  deployment.KeyPair
		want  string
	}{
		"previous secret alone": {githubAppFlags{}, previous.WebhookSecret(), "webhook secret"},
		"no webhook secret":     {full, deployment.KeyPair{}, "webhook secret"},
		"no private key":        {without(func(f *githubAppFlags) { f.PrivateKey = "" }), hook, "private key"},
		"no client secret":      {without(func(f *githubAppFlags) { f.ClientSecret = "" }), hook, "client id and secret"},
		"malformed key":         {without(func(f *githubAppFlags) { f.PrivateKey = "not a key" }), hook, "private key"},
		"app id not a number":   {without(func(f *githubAppFlags) { f.AppID = "abc" }), hook, "not a number"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := controller.New(nil, nil)
			configureGitHubApp(srv, c.flags, c.hook)
			response := httptest.NewRecorder()
			srv.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/webhooks/github-app", strings.NewReader(`{}`)))
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "GitHub App unavailable: ") || !strings.Contains(response.Body.String(), c.want) {
				t.Fatalf("App operation = %d %s, want unavailable with %q", response.Code, response.Body, c.want)
			}
		})
	}
}

func TestGitHubAppDocsDescribeNarrowFailures(t *testing.T) {
	page, err := docs.ReadRaw("github-app")
	if err != nil {
		t.Fatal(err)
	}
	for _, term := range []string{"GitHub App unavailable: <reason>", "reads every page", "repositories_removed", "failed", "inherited child runs"} {
		if !strings.Contains(page, term) {
			t.Errorf("GitHub App guide omits %q", term)
		}
	}
	for _, term := range []string{"refuses to start with some", "listing is read up to 1000", "first 1000"} {
		if strings.Contains(page, term) {
			t.Errorf("GitHub App guide retains %q", term)
		}
	}
}
