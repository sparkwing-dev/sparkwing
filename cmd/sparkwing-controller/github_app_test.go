package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

func TestConfigureGitHubApp(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	full := githubAppFlags{AppID: "4242", Slug: "sparkwing", ClientID: "Iv1.x", ClientSecret: "s", PrivateKey: pemText, WebhookSecret: "whsec"}

	if err := configureGitHubApp(controller.New(nil, nil), githubAppFlags{}); err != nil {
		t.Fatalf("nothing configured = %v, want the App left off", err)
	}
	srv := controller.New(nil, nil)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	if err := configureGitHubApp(srv, full); err != nil {
		t.Fatalf("complete configuration = %v", err)
	}
	without := func(edit func(*githubAppFlags)) githubAppFlags {
		f := full
		edit(&f)
		return f
	}
	cases := map[string]struct {
		flags githubAppFlags
		want  string
	}{
		"no webhook secret":   {without(func(f *githubAppFlags) { f.WebhookSecret = "" }), "webhook secret"},
		"no private key":      {without(func(f *githubAppFlags) { f.PrivateKey = "" }), "private key"},
		"no client secret":    {without(func(f *githubAppFlags) { f.ClientSecret = "" }), "client id and secret"},
		"app id not a number": {without(func(f *githubAppFlags) { f.AppID = "abc" }), "not a number"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := configureGitHubApp(controller.New(nil, nil), c.flags)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one naming %q", err, c.want)
			}
		})
	}
}
