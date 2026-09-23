package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

func TestConfigureGitHubApp(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "app.pem")
	pemText := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(keyFile, pemText, 0o600); err != nil {
		t.Fatal(err)
	}
	full := githubAppFlags{AppID: "4242", Slug: "sparkwing", ClientID: "Iv1.x", ClientSecret: "s"}
	env := func(vals map[string]string) func(string) string {
		return func(k string) string { return vals[k] }
	}
	complete := map[string]string{
		"SPARKWING_GITHUB_APP_PRIVATE_KEY_FILE": keyFile,
		"SPARKWING_GITHUB_APP_WEBHOOK_SECRET":   "whsec",
	}

	if err := configureGitHubApp(controller.New(nil, nil), githubAppFlags{}, env(nil)); err != nil {
		t.Fatalf("nothing configured = %v, want the App left off", err)
	}
	srv := controller.New(nil, nil)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	if err := configureGitHubApp(srv, full, env(complete)); err != nil {
		t.Fatalf("complete configuration = %v", err)
	}
	cases := map[string]struct {
		flags githubAppFlags
		env   map[string]string
		want  string
	}{
		"no webhook secret": {full, map[string]string{"SPARKWING_GITHUB_APP_PRIVATE_KEY_FILE": keyFile}, "webhook secret"},
		"no private key":    {full, map[string]string{"SPARKWING_GITHUB_APP_WEBHOOK_SECRET": "whsec"}, "private key"},
		"no client secret":  {githubAppFlags{AppID: "4242", Slug: "sparkwing", ClientID: "Iv1.x"}, complete, "client id and secret"},
		"key in both places": {full, map[string]string{
			"SPARKWING_GITHUB_APP_PRIVATE_KEY_FILE": keyFile, "SPARKWING_GITHUB_APP_PRIVATE_KEY": string(pemText),
			"SPARKWING_GITHUB_APP_WEBHOOK_SECRET": "whsec",
		}, "not both"},
		"app id not a number": {githubAppFlags{AppID: "abc", Slug: "sparkwing", ClientID: "Iv1.x", ClientSecret: "s"}, complete, "not a number"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := configureGitHubApp(controller.New(nil, nil), c.flags, env(c.env))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one naming %q", err, c.want)
			}
		})
	}
}
