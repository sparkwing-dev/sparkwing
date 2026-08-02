package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDecideInstall_MonotonicByVCSTime(t *testing.T) {
	newer := buildIdentity{Revision: "new", Time: time.Unix(200, 0)}
	older := buildIdentity{Revision: "old", Time: time.Unix(100, 0)}
	if got := decideInstall(older, newer, false); got != keepCurrent {
		t.Fatalf("older candidate decision=%v want keepCurrent", got)
	}
	if got := decideInstall(newer, older, false); got != installCandidate {
		t.Fatalf("newer candidate decision=%v want installCandidate", got)
	}
	if got := decideInstall(older, newer, true); got != installCandidate {
		t.Fatalf("explicit downgrade decision=%v want installCandidate", got)
	}
}

func TestInstallGuard_TwoWorktreesAlwaysLeaveNewerCLI(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "bin", "sparkwing")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	oldCandidate := filepath.Join(dir, "worktree-old", "sparkwing")
	newCandidate := filepath.Join(dir, "worktree-new", "sparkwing")
	writeCandidate(t, oldCandidate, "old")
	writeCandidate(t, newCandidate, "new")

	oldID := buildIdentity{Revision: "old", Time: time.Unix(100, 0)}
	newID := buildIdentity{Revision: "new", Time: time.Unix(200, 0)}
	start := make(chan struct{})
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	for _, item := range []struct {
		candidate string
		identity  buildIdentity
	}{{oldCandidate, oldID}, {newCandidate, newID}} {
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errCh <- withInstallLock(target, time.Second, func() error {
				_, err := installLocked(item.candidate, target, item.identity, false, readBuildIdentity)
				return err
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := readBuildIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != "new" {
		t.Fatalf("global binary identity=%+v want newer worktree candidate", got)
	}
}

func TestInstallLocked_OlderCandidateSkipsSuccessfully(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sparkwing")
	candidate := filepath.Join(dir, "older")
	writeCandidate(t, target, "new")
	writeCandidate(t, candidate, "old")
	read := func(path string) (buildIdentity, error) {
		if path == target {
			return buildIdentity{Revision: "new", Time: time.Unix(200, 0)}, nil
		}
		return buildIdentity{Revision: "old", Time: time.Unix(100, 0)}, nil
	}
	installed, err := installLocked(candidate, target, buildIdentity{
		Revision: "old", Time: time.Unix(100, 0),
	}, false, read)
	if err != nil {
		t.Fatalf("older consumer must continue compatibly: %v", err)
	}
	if installed {
		t.Fatal("older candidate reported installed")
	}
}

func TestInstallLocked_UnorderedCurrentFailsWithRecovery(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sparkwing")
	candidate := filepath.Join(dir, "candidate")
	writeCandidate(t, target, "current")
	writeCandidate(t, candidate, "candidate")
	unreadable := errors.New("no trustworthy VCS time")
	_, err := installLocked(candidate, target, buildIdentity{
		Revision: "candidate", Time: time.Unix(200, 0),
	}, false, func(string) (buildIdentity, error) {
		return buildIdentity{}, unreadable
	})
	if err == nil || !strings.Contains(err.Error(), "SPARKWING_INSTALL_ALLOW_DOWNGRADE=1") {
		t.Fatalf("error=%v, want explicit recovery guidance", err)
	}
}

func TestReadBuildIdentity_IgnoresSidecarAfterExternalOverwrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sparkwing")
	candidate := filepath.Join(dir, "candidate")
	writeCandidate(t, candidate, "new")
	_, err := installLocked(candidate, target, buildIdentity{
		Revision: "new", Time: time.Unix(200, 0),
	}, false, readBuildIdentity)
	if err != nil {
		t.Fatal(err)
	}
	// safety: simulate an older pre-guard installer overwriting only the binary and
	// leaving the new sidecar behind. The sidecar must not launder that file
	// into the newer identity.
	if err := os.WriteFile(target, []byte("not the guarded binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := readBuildIdentity(target); err == nil {
		t.Fatal("stale sidecar accepted after binary hash changed")
	}
}

func writeCandidate(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bodyBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = body
}
