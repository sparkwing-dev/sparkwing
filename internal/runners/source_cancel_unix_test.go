//go:build !windows

package runners

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

type cancelledSourceAccess struct{}

func (cancelledSourceAccess) GitCredential(context.Context, source.Spec) (source.Credential, error) {
	return source.Credential{}, nil
}

func (cancelledSourceAccess) BundleURL(context.Context, string) (string, error) {
	return "", errors.New("unexpected bundle read")
}

func TestRunnerSourceCancellationJoinsFetchAndRemovesCheckout(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	tools, workspace := t.TempDir(), t.TempDir()
	ready, block := filepath.Join(tools, "ready"), filepath.Join(tools, "block")
	for _, path := range []string{ready, block} {
		if err := unix.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	reader, err := os.OpenFile(ready, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	script := "#!/bin/sh\nfor arg do\n if [ \"$arg\" = fetch ]; then\n  printf x > \"$FETCH_READY\"\n  read ignored < \"$FETCH_BLOCK\"\n  exit 1\n fi\ndone\nexec \"$REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(tools, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	reached := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(reader, make([]byte, 1))
		cancel()
		reached <- err
	}()
	entry, err := source.PrepareCheckout(ctx, source.Spec{
		RepoKey: "git:8.8.8.8/team/repo", RemoteURL: "https://8.8.8.8/team/repo", SHA: "0123456789abcdef0123456789abcdef01234567",
	}, source.Options{Workspace: workspace, Access: cancelledSourceAccess{}, BuildEnvironment: func(procenv.Kind, procenv.Base, procenv.Runtime) ([]string, error) {
		return []string{"PATH=" + os.Getenv("PATH"), "REAL_GIT=" + realGit, "FETCH_READY=" + ready, "FETCH_BLOCK=" + block}, nil
	}})
	if entry != nil {
		err = errors.Join(err, entry.Close(), errors.New("cancelled fetch returned an entry"))
	}
	reader.Close()
	if reachedErr := <-reached; reachedErr != nil {
		t.Fatalf("fetch never started: %v; source error: %v", reachedErr, err)
	}
	if !errors.Is(err, context.Canceled) || entry != nil {
		t.Fatalf("cancelled fetch: %v", err)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil || len(entries) != 0 {
		t.Fatalf("fetch workspace survived cancellation: %v %v", entries, err)
	}
}
