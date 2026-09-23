package cluster

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A runner with neither a git cache nor an allow list holds no git
// credential of its own. This drives one run end to end against a real
// controller: the team stored an ssh deploy key for the repository's host and
// opted the runner's machine in, the runner asks for the run's credential,
// fetches over ssh with only that key and the pinned host key, compiles the
// pipeline and runs it. The pipeline itself finds the key file gone, and the
// controller recorded the release.
func TestCloudRunnerFetchesWithTheTeamsDeployKey(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: fetches over a stand-in ssh transport and compiles a pipeline")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_GITCACHE_URL", "")
	t.Setenv(authwire.CacheGrantEnv, "")
	t.Setenv("SSH_AUTH_SOCK", "")

	origins, record := t.TempDir(), t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	sha := makeKeyCheckOrigin(t, filepath.Join(origins, "acme", "private.git"), filepath.Join(record, "keypath"), marker)
	stubSSH(t, origins, record)
	const host, repoURL = "git.example.invalid", "ssh://git@git.example.invalid/acme/private.git"

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	token, tok, err := st.CreateToken("pool", store.TokenKindRunner, []string{
		controller.ScopeTriggersClaim, controller.ScopeNodesClaim, controller.ScopeRunsRead,
		controller.ScopeRunsState, controller.ScopeRunsWrite,
	}, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	ctrlSrv := httptest.NewServer(controller.New(st, discardLogger()).WithSecretsCipher(cipher).EnableAuthFromStore().Handler())
	t.Cleanup(ctrlSrv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	tn, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	deployKey := storeDeployKey(t, ctx, tn, cipher, host)
	if err := tn.SetGitCredentialMachine(ctx, tok.Prefix, true, "owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := tn.CreateTrigger(ctx, store.Trigger{
		ID: "cloud-run", Pipeline: "deploy", RepoURL: repoURL, GitBranch: "main", GitSHA: sha,
	}); err != nil {
		t.Fatal(err)
	}

	loopDone := make(chan error, 1)
	loopCtx, stopLoop := context.WithCancel(ctx)
	defer stopLoop()
	go func() {
		loopDone <- RunTriggerLoop(loopCtx, TriggerLoopOptions{
			ControllerURL: ctrlSrv.URL,
			Token:         token,
			WorkRoot:      t.TempDir(),
			Poll:          50 * time.Millisecond,
			Logger:        discardLogger(),
			MaxConcurrent: 1,
		})
	}()
	deadline := time.After(2 * time.Minute)
	for {
		if got, err := os.ReadFile(marker); err == nil && len(got) > 0 {
			if string(got) != "key-gone" {
				t.Fatalf("the pipeline saw %q, want the deploy key gone before it ran", got)
			}
			break
		}
		select {
		case err := <-loopDone:
			t.Fatalf("trigger loop stopped before the pipeline ran: %v", err)
		case <-deadline:
			t.Fatal("the pipeline never ran from the ssh source")
		case <-time.After(50 * time.Millisecond):
		}
	}
	stopLoop()
	if err := <-loopDone; err != nil {
		t.Fatalf("trigger loop: %v", err)
	}

	if got := readFileTrim(t, filepath.Join(record, "keybody")); got != strings.TrimSpace(deployKey) {
		t.Fatal("ssh was not handed the team's deploy key")
	}
	if env := readFileTrim(t, filepath.Join(record, "env")); strings.Contains(env, "PRIVATE KEY") {
		t.Fatal("the deploy key reached ssh's environment")
	}
	rels, err := tn.GitCredentialReleases(ctx, 10)
	if err != nil || len(rels) != 1 || rels[0].RunID != "cloud-run" || rels[0].TokenPrefix != tok.Prefix {
		t.Fatalf("releases = %+v, %v; want one for the run and this runner", rels, err)
	}
}

func storeDeployKey(t *testing.T, ctx context.Context, tn *store.Tenant, cipher *secrets.Cipher, host string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	pemKey := string(pem.EncodeToMemory(block))
	hostPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := ssh.NewPublicKey(hostPub)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := cipher.SealBound(string(tn.Team()), "git-credential:"+host, "git-credential", false, true, pemKey)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := ssh.FingerprintSHA256(hostKey)
	if _, err := tn.PutGitCredential(ctx, store.GitCredential{
		Host: host, Kind: store.GitCredentialSSH, Secret: sealed, Fingerprint: fingerprint,
		KnownHosts: knownhosts.Line([]string{host}, hostKey),
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := tn.ConfirmGitCredential(ctx, host, fingerprint, time.Now()); err != nil {
		t.Fatal(err)
	}
	return pemKey
}

// makeKeyCheckOrigin is a repository whose pipeline, when it runs, writes
// "key-gone" to marker if the deploy key file named in keypathFile no longer
// exists, and "key-present" if it does.
func makeKeyCheckOrigin(t *testing.T, bare, keypathFile, marker string) string {
	t.Helper()
	work := t.TempDir()
	pipeline := filepath.Join(work, ".sparkwing")
	if err := os.MkdirAll(pipeline, 0o755); err != nil {
		t.Fatal(err)
	}
	main := fmt.Sprintf(`package main

import (
	"os"
	"strings"
)

func main() {
	raw, err := os.ReadFile(%q)
	if err != nil {
		os.Exit(1)
	}
	state := "key-gone"
	if _, err := os.Stat(strings.TrimSpace(string(raw))); err == nil {
		state = "key-present"
	}
	if err := os.WriteFile(%q, []byte(state), 0o644); err != nil {
		os.Exit(1)
	}
}
`, keypathFile, marker)
	files := map[string]string{"go.mod": "module example.com/pipeline\n\ngo 1.22\n", "main.go": main}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(pipeline, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(work, "init", "--quiet", "--initial-branch=main")
	git(work, "add", ".")
	git(work, "commit", "--quiet", "-m", "pipeline")
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		t.Fatal(err)
	}
	git(work, "clone", "--quiet", "--bare", work, bare)
	return git(work, "rev-parse", "HEAD")
}

// stubSSH puts an ssh stand-in first on PATH. It records the key file it is
// handed (-i), that file's body and its own environment under record, then
// serves the repository under root the way sshd would run git-upload-pack.
func stubSSH(t *testing.T, root, record string) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
rec='` + record + `'
env > "$rec/env"
key=""; prev=""
for a; do
  if [ "$prev" = "-i" ]; then key="$a"; fi
  prev="$a"
done
printf '%s\n' "$key" > "$rec/keypath"
cat "$key" > "$rec/keybody"
for last; do :; done
exec sh -c "$(printf '%s' "$last" | sed "s#'/#'` + root + `/#")"
`
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("GIT_SSH", "")
}

func readFileTrim(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}
