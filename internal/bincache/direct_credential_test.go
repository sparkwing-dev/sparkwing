package bincache

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testDeployKey = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACBxZXN0a2V5bWF0ZXJpYWxub3RyZWFsbHlhbmVkMjU1MTlrZXkAAAAAAAAA
-----END OPENSSH PRIVATE KEY-----`

const testKnownHosts = "git.example.invalid ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHRlc3Rob3N0a2V5"

func credentialController(t *testing.T, status int, body string) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer runner-tok" {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/source-token") {
			_, _ = w.Write([]byte(`{"token":"ghs_legacy"}`))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &paths
}

func TestRequestDirectCredentialReadsEachKind(t *testing.T) {
	cases := map[string]struct {
		body string
		want DirectCredential
	}{
		"app": {
			`{"kind":"github_app","host":"github.com","token":"ghs_ok"}`,
			DirectCredential{Kind: CredentialGitHubApp, Host: "github.com", Username: "x-access-token", Secret: "ghs_ok"},
		},
		"https": {
			`{"kind":"https","host":"gitlab.example.com","username":"deploy","secret":"glpat-abc"}`,
			DirectCredential{Kind: CredentialHTTPS, Host: "gitlab.example.com", Username: "deploy", Secret: "glpat-abc"},
		},
		"ssh": {
			`{"kind":"ssh","host":"git.example.invalid","secret":` + jsonString(testDeployKey) +
				`,"known_hosts":` + jsonString(testKnownHosts) + `}`,
			DirectCredential{Kind: CredentialSSH, Host: "git.example.invalid", Secret: testDeployKey, KnownHosts: testKnownHosts},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv, paths := credentialController(t, http.StatusOK, tc.body)
			got, err := RequestDirectCredential(context.Background(), srv.URL, "runner-tok", "run-1", nil)
			if err != nil || got != tc.want {
				t.Fatalf("credential = %+v, %v; want %+v", got, err, tc.want)
			}
			if len(*paths) != 1 || (*paths)[0] != "/api/v1/runs/run-1/git-credential" {
				t.Fatalf("asked %v, want only the git-credential route", *paths)
			}
		})
	}
}

func TestRequestDirectCredentialNamesTheRemedy(t *testing.T) {
	srv, paths := credentialController(t, http.StatusNotFound,
		`{"error":"no_source_credential","message":"install the GitHub App or store a git credential for gitlab.example.com"}`)
	_, err := RequestDirectCredential(context.Background(), srv.URL, "runner-tok", "run-1", nil)
	if !errors.Is(err, ErrNoSourceCredential) || !strings.Contains(err.Error(), "store a git credential") {
		t.Fatalf("err = %v, want ErrNoSourceCredential carrying the remedy", err)
	}
	if len(*paths) != 1 {
		t.Fatalf("asked %v, want no fallback to the source-token route", *paths)
	}
}

// A controller from before the git-credential route answers its mux's plain
// 404, and the runner asks for the App source token instead.
func TestRequestDirectCredentialFallsBackOnAnOldController(t *testing.T) {
	srv, paths := credentialController(t, http.StatusNotFound, "404 page not found\n")
	got, err := RequestDirectCredential(context.Background(), srv.URL, "runner-tok", "run-1", nil)
	if err != nil || got.Kind != CredentialGitHubApp || got.Secret != "ghs_legacy" || got.Host != "github.com" {
		t.Fatalf("credential = %+v, %v; want the legacy App token", got, err)
	}
	if len(*paths) != 2 || !strings.HasSuffix((*paths)[1], "/source-token") {
		t.Fatalf("asked %v, want git-credential then source-token", *paths)
	}
}

func TestRequestDirectCredentialRefusesUnusableValues(t *testing.T) {
	for name, body := range map[string]string{
		"app token newline":   `{"kind":"github_app","host":"github.com","token":"ghs_ok\nGIT_CONFIG_KEY_9=x"}`,
		"app on another host": `{"kind":"github_app","host":"gitlab.com","token":"ghs_ok"}`,
		"https space":         `{"kind":"https","host":"gitlab.com","username":"u","secret":"a b"}`,
		"https newline user":  `{"kind":"https","host":"gitlab.com","username":"u\nhost=evil","secret":"ab"}`,
		"host with a path":    `{"kind":"https","host":"gitlab.com/evil","username":"u","secret":"ab"}`,
		"ssh without pin":     `{"kind":"ssh","host":"gitlab.com","secret":` + jsonString(testDeployKey) + `}`,
		"ssh not a key":       `{"kind":"ssh","host":"gitlab.com","secret":"hunter2","known_hosts":"gitlab.com ssh-ed25519 AAAA"}`,
		"unknown kind":        `{"kind":"ftp","host":"gitlab.com","secret":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := credentialController(t, http.StatusOK, body)
			if got, err := RequestDirectCredential(context.Background(), srv.URL, "runner-tok", "run-1", nil); err == nil {
				t.Fatalf("accepted %+v", got)
			}
		})
	}
}

// A runner its owner did not fence has no credentials of its own to offer:
// when the controller releases none it fails with the remedy and never
// fetches. The owner-fenced runner is the control, and fetches with its own.
func TestCloudRunnerHasNoMachineCredentialFallback(t *testing.T) {
	srv, _ := credentialController(t, http.StatusNotFound,
		`{"error":"no_source_credential","message":"install the GitHub App on acme/widgets"}`)
	for _, owner := range []bool{false, true} {
		fetched := false
		var used DirectCredential
		_, err := fetchRunSourceDirect(context.Background(), RunSource{
			ControllerURL: srv.URL, RunnerToken: "runner-tok", RunID: "run-1", OwnerCredentials: owner,
		}, nil, func(cred DirectCredential) (string, error) {
			fetched, used = true, cred
			return "sparkwing", nil
		})
		switch {
		case !owner && (err == nil || fetched):
			t.Fatalf("an unfenced runner fetched (err=%v, fetched=%v); want a refusal before any fetch", err, fetched)
		case !owner && !strings.Contains(err.Error(), "install the GitHub App"):
			t.Fatalf("err = %v, want the controller's remedy", err)
		case owner && (err != nil || !fetched || !used.Empty()):
			t.Fatalf("control: an owner-fenced runner = %v fetched=%v cred=%+v; want its own credentials", err, fetched, used)
		}
	}
}

// A released credential is the only one the fetch presents: the machine's own
// credential helper and http.extraHeader, which would both authenticate, are
// never read, so a wrong released token fails rather than falling through to
// the machine's.
func TestReleasedCredentialFetchReadsNoAmbientCredential(t *testing.T) {
	repos := t.TempDir()
	makeBareRepoWithSparkwing(t, repos, "app", "main")
	const good = "glpat-good"
	srv, seen := authGitServer(t, repos, good)
	remote := srv.URL + "/app.git"

	global := filepath.Join(t.TempDir(), "gitconfig")
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + good))
	writeTestFile(t, global, "[credential]\n\thelper = \"!f() { echo username=x-access-token; echo password="+good+"; }; f\"\n"+
		"[http]\n\textraHeader = Authorization: Basic "+basic+"\n")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("SSH_AUTH_SOCK", "/tmp/should-not-be-used.sock")

	host := strings.TrimPrefix(srv.URL, "http://")
	wrong := DirectCredential{Kind: CredentialHTTPS, Host: host, Username: "x-access-token", Secret: "glpat-wrong"}
	opts := httpOnly
	opts.cred = wrong
	err := directCheckout(context.Background(), t.TempDir(), remote, "main", "", filepath.Join(t.TempDir(), "run"), opts)
	if err == nil || seen.Load() != 0 {
		t.Fatalf("a wrong released token fetched through the machine's helper: err=%v, authorized=%d", err, seen.Load())
	}
	if strings.Contains(err.Error(), "glpat-wrong") {
		t.Fatalf("the failed fetch's error carries the token: %v", err)
	}

	if err := directCheckout(context.Background(), t.TempDir(), remote, "main", "",
		filepath.Join(t.TempDir(), "run"), httpOnly); err != nil || seen.Load() == 0 {
		t.Fatalf("control: the machine's own helper fetch = %v, authorized=%d", err, seen.Load())
	}

	seen.Store(0)
	opts.cred = DirectCredential{Kind: CredentialHTTPS, Host: host, Username: "x-access-token", Secret: good}
	if err := directCheckout(context.Background(), t.TempDir(), remote, "main", "",
		filepath.Join(t.TempDir(), "run"), opts); err != nil || seen.Load() == 0 {
		t.Fatalf("the right released token = %v, authorized=%d", err, seen.Load())
	}
}

// sshRecorder is an ssh stand-in named ssh on PATH. It records its argv, its
// environment, the key file's mode and body, and the pinned known_hosts, then
// serves the repository under root the way sshd would run git-upload-pack.
// With echoKey set it prints the key to stderr and fails instead.
func sshRecorder(t *testing.T, root string, echoKey bool) (record string) {
	t.Helper()
	bin, record := t.TempDir(), t.TempDir()
	serve := `for last; do :; done
exec sh -c "$(printf '%s' "$last" | sed "s#'/#'` + root + `/#")"`
	if echoKey {
		serve = `cat "$key" >&2
exit 1`
	}
	script := `#!/bin/sh
rec='` + record + `'
printf '%s\n' "$@" > "$rec/args"
env > "$rec/env"
key=""; hosts=""; prev=""
for a; do
  if [ "$prev" = "-i" ]; then key="$a"; fi
  case "$a" in UserKnownHostsFile=*) hosts="${a#UserKnownHostsFile=}";; esac
  prev="$a"
done
printf '%s' "$key" > "$rec/keypath"
stat -c %a "$key" > "$rec/keymode" 2>/dev/null || stat -f %Lp "$key" > "$rec/keymode"
cat "$key" > "$rec/keybody"
cat "$hosts" > "$rec/hosts"
` + serve + "\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

func readRecord(t *testing.T, record, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(record, name))
	if err != nil {
		t.Fatalf("the ssh stand-in never recorded %s: %v", name, err)
	}
	return strings.TrimSpace(string(raw))
}

// The deploy key reaches ssh as a 0600 file with the pinned host key beside
// it, through options that admit no other identity, agent or known host. It
// is in no environment variable, and it is gone when the checkout returns,
// before anything the fetched tree names could run.
func TestSSHCredentialFetchPinsTheKeyAndRemovesIt(t *testing.T) {
	repos := t.TempDir()
	_, tip := makeBareRepoWithSparkwing(t, repos, "widgets", "main")
	record := sshRecorder(t, repos, false)
	t.Setenv("GIT_SSH_COMMAND", "/usr/bin/false")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/should-not-be-used.sock")

	cred := DirectCredential{Kind: CredentialSSH, Host: "git.example.invalid", Secret: testDeployKey, KnownHosts: testKnownHosts}
	opts := directOptions{protocols: "ssh", cred: cred}
	dest := filepath.Join(t.TempDir(), "run")
	if err := directCheckout(context.Background(), t.TempDir(), "ssh://git@git.example.invalid/widgets.git",
		"main", tip, dest, opts); err != nil {
		t.Fatalf("ssh fetch with the deploy key: %v", err)
	}
	if got := readMarker(t, dest); got == "" {
		t.Fatal("the checkout holds no tree")
	}
	keyPath := readRecord(t, record, "keypath")
	if got := readRecord(t, record, "keymode"); got != "600" {
		t.Errorf("key file mode = %s, want 600", got)
	}
	if got := readRecord(t, record, "keybody"); got != testDeployKey {
		t.Errorf("key file = %q, want the released key", got)
	}
	if got := readRecord(t, record, "hosts"); got != testKnownHosts {
		t.Errorf("known_hosts = %q, want only the pinned entry", got)
	}
	args := readRecord(t, record, "args") + "\n"
	for _, want := range []string{
		"IdentitiesOnly=yes", "StrictHostKeyChecking=yes", "IdentityAgent=none",
		"GlobalKnownHostsFile=/dev/null", "BatchMode=yes", "ForwardAgent=no",
	} {
		if !strings.Contains(args, want+"\n") {
			t.Errorf("ssh argv lacks %s:\n%s", want, args)
		}
	}
	env := readRecord(t, record, "env")
	if strings.Contains(env, "PRIVATE KEY") || strings.Contains(env, "b3BlbnNzaC1rZXktdjEAAAAABG5vbmUA") {
		t.Error("the key is in ssh's environment")
	}
	if strings.Contains(env, "should-not-be-used") {
		t.Error("the machine's ssh agent reached the fetch")
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("the key file %s outlived the checkout: %v", keyPath, err)
	}
	if _, err := os.Stat(filepath.Dir(keyPath)); !os.IsNotExist(err) {
		t.Fatalf("the key directory outlived the checkout: %v", err)
	}
}

// A transport that echoes the key into its error output has the key redacted
// from the error that carries that output to logs and the run's failure.
func TestSSHCredentialLeakIsRedactedFromTheError(t *testing.T) {
	repos := t.TempDir()
	_, tip := makeBareRepoWithSparkwing(t, repos, "widgets", "main")
	record := sshRecorder(t, repos, true)
	cred := DirectCredential{Kind: CredentialSSH, Host: "git.example.invalid", Secret: testDeployKey, KnownHosts: testKnownHosts}
	err := directCheckout(context.Background(), t.TempDir(), "ssh://git@git.example.invalid/widgets.git",
		"main", tip, filepath.Join(t.TempDir(), "run"), directOptions{protocols: "ssh", cred: cred})
	if err == nil {
		t.Fatal("the failing ssh stand-in served a fetch")
	}
	if !strings.Contains(err.Error(), "***") {
		t.Fatalf("control: the stand-in's echo never reached the error: %v", err)
	}
	for _, line := range strings.Split(testDeployKey, "\n")[1:3] {
		if strings.Contains(err.Error(), line) {
			t.Fatalf("the error carries key material %q:\n%v", line, err)
		}
	}
	if _, statErr := os.Stat(readRecord(t, record, "keypath")); !os.IsNotExist(statErr) {
		t.Fatalf("the key file outlived a failed fetch: %v", statErr)
	}
}

func TestCredentialRemoteFitsTheTransportAndHost(t *testing.T) {
	https := DirectCredential{Kind: CredentialHTTPS, Host: "gitlab.example.com", Username: "u", Secret: "s"}
	ssh := DirectCredential{Kind: CredentialSSH, Host: "gitlab.example.com", Secret: testDeployKey, KnownHosts: "x"}
	cases := []struct {
		remote string
		cred   DirectCredential
		want   string
	}{
		{"git@gitlab.example.com:Acme/Widgets.git", https, "https://gitlab.example.com/Acme/Widgets.git"},
		{"ssh://git@gitlab.example.com:2222/acme/w.git", https, "https://gitlab.example.com/acme/w.git"},
		{"https://gitlab.example.com/acme/w.git", https, "https://gitlab.example.com/acme/w.git"},
		{"https://gitlab.example.com/Acme/w.git", ssh, "ssh://git@gitlab.example.com/Acme/w.git"},
		{"git@gitlab.example.com:acme/w.git", ssh, "git@gitlab.example.com:acme/w.git"},
	}
	for _, tc := range cases {
		got, err := credentialRemote(tc.remote, tc.cred)
		if err != nil || got != tc.want {
			t.Errorf("credentialRemote(%q, %s) = %q, %v; want %q", tc.remote, tc.cred.Kind, got, err, tc.want)
		}
	}
	if _, err := credentialRemote("https://evil.example.com/acme/w.git", https); err == nil ||
		!strings.Contains(err.Error(), "bound to gitlab.example.com") {
		t.Fatalf("a remote on another host = %v, want a refusal", err)
	}
}

func jsonString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}
