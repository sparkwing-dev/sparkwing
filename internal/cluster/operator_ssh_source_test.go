package cluster

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// The operator's runners build its private repositories, which the cache
// clones over SSH with its own key. A runner reaches the cache only with the
// run's grant, so this drives one operator run end to end: the controller
// mints a grant for the run's team, the runner registers and clones the SSH
// mirror with it, compiles the pipeline it holds and runs it. Another team's
// grant from the same key then cannot clone that mirror.
func TestOperatorRunBuildsFromAnSSHMirrorThroughItsGrant(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: clones over a stand-in SSH transport and compiles a pipeline")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv(authwire.CacheGrantEnv, "")

	origins := t.TempDir()
	ran := make(chan []byte, 1)
	marker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ran <- data
	}))
	t.Cleanup(marker.Close)
	sha := makePrivateOrigin(t, filepath.Join(origins, "acme", "private.git"), marker.URL)
	const repoURL = "ssh://git@git.example.invalid/acme/private.git"
	standInSSH(t, origins)

	cacheSrv := newGrantCache(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctrlSrv := httptest.NewServer(controller.New(st, nil).WithCacheURL(cacheSrv.URL).WithCacheGrantKey(cacheGrantKey).Handler())
	t.Cleanup(ctrlSrv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: "operator-run", Pipeline: "deploy", RepoURL: repoURL, GitBranch: "main", GitSHA: sha,
	}); err != nil {
		t.Fatal(err)
	}

	loopDone := make(chan error, 1)
	loopCtx, stopLoop := context.WithCancel(ctx)
	defer stopLoop()
	go func() {
		loopDone <- RunTriggerLoop(loopCtx, TriggerLoopOptions{
			ControllerURL: ctrlSrv.URL,
			GitcacheURL:   cacheSrv.URL,
			WorkRoot:      t.TempDir(),
			Poll:          50 * time.Millisecond,
			Logger:        discardLogger(),
			MaxConcurrent: 1,
		})
	}()
	select {
	case got := <-ran:
		if string(got) != toyProtocolArgv {
			t.Fatalf("the pipeline ran with %q, want the run host's %q", got, toyProtocolArgv)
		}
	case err := <-loopDone:
		t.Fatalf("trigger loop stopped before the pipeline ran: %v", err)
	case <-ctx.Done():
		t.Fatal("the operator's pipeline never ran from its SSH mirror")
	}
	stopLoop()
	if err := <-loopDone; err != nil {
		t.Fatalf("trigger loop: %v", err)
	}

	other, err := mintTestCacheGrant("team-a", "team-a-run")
	if err != nil {
		t.Fatal(err)
	}
	_, err = bincache.FetchPipelineSourceWithCredentials(ctx, cacheSrv.URL, ctrlSrv.URL, "", other,
		repoURL, "main", sha, t.TempDir())
	if err == nil {
		t.Fatal("another team's grant cloned the operator's SSH mirror")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("another team's grant failed with %v, want the cache's 404 for a mirror it may not read", err)
	}
}

func makePrivateOrigin(t *testing.T, bare, marker string) string {
	t.Helper()
	work := t.TempDir()
	pipeline := filepath.Join(work, ".sparkwing")
	if err := os.MkdirAll(pipeline, 0o755); err != nil {
		t.Fatal(err)
	}
	main := fmt.Sprintf(`package main

import (
	"net/http"
	"os"
	"strings"
)

func main() {
	const marker = %q
	value := strings.Join(os.Args[1:], " ")
	if strings.HasPrefix(marker, "http://") {
		response, err := http.Post(marker, "text/plain", strings.NewReader(value))
		if err != nil {
			os.Exit(1)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	if err := os.WriteFile(marker, []byte(value), 0o644); err != nil {
		os.Exit(1)
	}
}
`, marker)
	files := map[string]string{
		"go.mod":  "module example.com/pipeline\n\ngo 1.22\n",
		"main.go": main,
	}
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

func standInSSH(t *testing.T, root string) {
	t.Helper()
	// hack: git runs the remote command through this script instead of sshd, so the cache's ssh:// clone path
	// runs for real with no daemon; "simple" keeps git from probing the script for OpenSSH options.
	script := filepath.Join(t.TempDir(), "ssh")
	body := "#!/bin/sh\n" +
		"for last; do :; done\n" +
		"exec sh -c \"$(printf '%s' \"$last\" | sed \"s#'/#'" + filepath.ToSlash(root) + "/#\")\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", "'"+strings.ReplaceAll(filepath.ToSlash(script), "'", "'\\''")+"'")
	t.Setenv("GIT_SSH_VARIANT", "simple")
}
