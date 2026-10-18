package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

// A run's compile and binary-cache fill stay on this machine even when the
// shell exports a profile, a secrets profile and controller URLs.
func TestMaterialize_ColdCacheContactsNoController(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: compiles a pipeline module; the fast class runs under -short")
	}
	if os.Getenv("SPARKWING_TEST_OFFLINE_CHILD") == "1" {
		directory := os.Getenv("SPARKWING_TEST_OFFLINE_DIR")
		home := os.Getenv("SPARKWING_HOME")
		options, err := hostSourceOptions(home, os.Environ(), secrets.NewMasker(), true)
		if err == nil {
			var entry *source.Entry
			entry, err = source.Acquire(context.Background(), source.Spec{RepoKey: "local:" + filepath.Dir(directory), LocalCheckout: filepath.Dir(directory), PipelineDir: ".sparkwing"}, options)
			if entry != nil {
				err = errors.Join(err, entry.Close())
			}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "materialize: %v\n", err)
			os.Exit(19)
		}
		os.Exit(0)
	}

	var requests atomic.Int32
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer controller.Close()

	repo := initSnapshotRepo(t)
	moduleDir := filepath.Join(repo, ".sparkwing")
	if err := os.Mkdir(moduleDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSDKFixture(t, moduleDir, "package main\nfunc main(){}\n")
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-qm", "fixture")
	config := filepath.Join(t.TempDir(), "config.yaml")
	body := fmt.Sprintf("profiles:\n  remote:\n    controller: { url: %s, token: swu_test }\n", controller.URL)
	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestMaterialize_ColdCacheContactsNoController$")
	cmd.Env = append(os.Environ(),
		"SPARKWING_TEST_OFFLINE_CHILD=1",
		"SPARKWING_TEST_OFFLINE_DIR="+moduleDir,
		"SPARKWING_HOME="+filepath.Join(t.TempDir(), "home"),
		"SPARKWING_CONFIG="+config,
		"SPARKWING_CONTROLLER_URL="+controller.URL,
		"SPARKWING_GITCACHE_URL=",
		"GOWORK=off",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("a cold-cache run sent %d requests to the controller", n)
	}
}
