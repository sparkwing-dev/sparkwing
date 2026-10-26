package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func TestConfigureGitHubApp(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	full := githubAppFlags{AppID: "4242", Slug: "sparkwing", ClientID: "Iv1.x", ClientSecret: "s", PrivateKey: pemText, RedirectURIs: "https://dashboard.example/callback"}
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
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "GitHub App unavailable") || strings.Contains(response.Body.String(), c.want) {
				t.Fatalf("App operation = %d %s, want generic unavailable hiding %q", response.Code, response.Body, c.want)
			}
		})
	}
}

func TestGitHubAppDocsDescribeNarrowFailures(t *testing.T) {
	page, err := docs.ReadRaw("github-app")
	if err != nil {
		t.Fatal(err)
	}
	for _, term := range []string{"GitHub App unavailable", "health and doctor warn", "configuration details stay in controller logs", "reads every page", "one-minute deadline", "empty or unreadable", "approved repository-ID set", "repositories_removed", "failed", "inherited child runs"} {
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

func TestGitHubAppStartupAvailability(t *testing.T) {
	const helper = "--github-app-startup"
	for i, arg := range os.Args {
		if arg == helper {
			if err := run(os.Args[i+1:]); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	for _, tc := range []struct {
		name, credential, value, redirect, want string
		args                                    []string
	}{
		{name: "missing redirects", want: "oauth-redirect-uris"},
		{name: "invalid redirects", redirect: "not-a-url", want: "oauth-redirect-uris"},
		{name: "insecure redirects", redirect: "http://dashboard.example/callback", want: "oauth-redirect-uris"},
		{name: "empty key", credential: credGitHubAppKey, want: "github-app-key"},
		{name: "empty webhook", credential: credGitHubAppWebhookSecret, want: "github-app-webhook-secret"},
		{name: "empty previous webhook", credential: credGitHubAppPreviousHook, want: "github-app-webhook-secret.previous"},
		{name: "empty OAuth secret", credential: credGitHubClientSecret, want: "github-client-secret"},
		{name: "bad key", credential: credGitHubAppKey, value: "bad-key", want: "private key"},
		{name: "missing OAuth ID", args: []string{"--github-client-id="}, want: "github-client-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{credGitHubAppKey: pemText, credGitHubAppWebhookSecret: "hook", credGitHubClientSecret: "client-secret"}
			if tc.credential != "" {
				files[tc.credential] = tc.value
			}
			redirect := "https://dashboard.example/callback"
			if tc.credential == "" && len(tc.args) == 0 {
				redirect = tc.redirect
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			args := []string{
				"-test.run=^TestGitHubAppStartupAvailability$", "--", helper,
				"--addr=127.0.0.1:0", "--github-app-id=4242", "--github-app-slug=sparkwing",
				"--github-client-id=Iv1.x", "--credentials-dir=" + credentialsDir(t, files), "--oauth-redirect-uris=" + redirect,
			}
			cmd := exec.CommandContext(ctx, os.Args[0], append(args, tc.args...)...)
			cmd.Env = append(os.Environ(), "SPARKWING_HOME="+t.TempDir())
			stderr, err := cmd.StderrPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			listening, done := make(chan string, 1), make(chan struct{})
			var logs strings.Builder
			go func() {
				scanner := bufio.NewScanner(stderr)
				for scanner.Scan() {
					line := scanner.Text()
					logs.WriteString(line + "\n")
					if strings.Contains(line, "controller listening") {
						_, addr, _ := strings.Cut(line, "addr=")
						listening <- strings.Trim(strings.Fields(addr)[0], `"`)
					}
				}
				_ = cmd.Wait()
				close(done)
			}()
			t.Cleanup(func() { cancel(); <-done })
			var addr string
			select {
			case addr = <-listening:
			case <-done:
				t.Fatalf("controller failed startup: %s", &logs)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			for _, path := range []string{"/api/v1/health", "/webhooks/github-app"} {
				method := http.MethodGet
				if path == "/webhooks/github-app" {
					method = http.MethodPost
				}
				req, err := http.NewRequestWithContext(ctx, method, "http://"+addr+path, strings.NewReader(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				wantStatus := http.StatusServiceUnavailable
				if path == "/api/v1/health" {
					wantStatus = http.StatusOK
				}
				named := strings.Contains(string(body), "GitHub App unavailable") == (path != "/api/v1/health")
				if err != nil || resp.StatusCode != wantStatus || !named || strings.Contains(string(body), tc.want) {
					t.Fatalf("%s = %d %s, %v; want generic unavailable hiding %q", path, resp.StatusCode, body, err, tc.want)
				}
			}
		})
	}
}

func TestGitHubOnlyInvalidRedirectKeepsControllerAvailable(t *testing.T) {
	for _, redirect := range []string{"", "not-a-url", "http://dashboard.example/callback"} {
		t.Run(redirect, func(t *testing.T) {
			cfg, err := parseIdentity(identityFlags{
				SignUpGate:   "open",
				GitHubApp:    githubAppFlags{ClientID: "client", ClientSecret: "secret"},
				RedirectURIs: redirect,
			})
			if err != nil {
				t.Fatalf("GitHub-only configuration stopped controller startup: %v", err)
			}
			st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			srv := controller.New(st, nil)
			configureIdentity(srv, cfg, slog.Default())
			for _, path := range []string{"/api/v1/health", "/webhooks/github-app"} {
				method, status := http.MethodGet, http.StatusOK
				if path == "/webhooks/github-app" {
					method, status = http.MethodPost, http.StatusServiceUnavailable
				}
				w := httptest.NewRecorder()
				srv.Handler().ServeHTTP(w, httptest.NewRequest(method, path, nil))
				named := strings.Contains(w.Body.String(), "GitHub App unavailable") == (path != "/api/v1/health")
				if w.Code != status || !named || strings.Contains(w.Body.String(), "oauth-redirect-uris") {
					t.Fatalf("%s: HTTP %d %s", path, w.Code, w.Body)
				}
			}
		})
	}
}
