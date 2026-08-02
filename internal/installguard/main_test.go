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
	equalTimePeer := buildIdentity{Revision: "peer", Time: newer.Time}
	if got := decideInstall(equalTimePeer, newer, false); got != unorderedBuilds {
		t.Fatalf("equal-time peer decision=%v want unorderedBuilds", got)
	}
	if got := decideInstall(equalTimePeer, newer, true); got != installCandidate {
		t.Fatalf("equal-time peer override decision=%v want installCandidate", got)
	}
}

func TestInstallGuard_ConcurrentEqualTimeRevisionsFailClosed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "bin", "sparkwing")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	candidates := []struct {
		path string
		id   buildIdentity
	}{
		{filepath.Join(dir, "worktree-a", "sparkwing"), buildIdentity{Revision: "revision-a", Time: time.Unix(200, 0)}},
		{filepath.Join(dir, "worktree-b", "sparkwing"), buildIdentity{Revision: "revision-b", Time: time.Unix(200, 0)}},
	}
	for _, candidate := range candidates {
		writeCandidate(t, candidate.path, candidate.id.Revision)
	}

	start := make(chan struct{})
	errCh := make(chan error, len(candidates))
	var wg sync.WaitGroup
	for _, candidate := range candidates {
		candidate := candidate
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errCh <- withInstallLock(target, time.Second, func() error {
				_, err := installLocked(candidate.path, target, candidate.id, false, readBuildIdentity)
				return err
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)

	var failures int
	for err := range errCh {
		if err == nil {
			continue
		}
		failures++
		if !strings.Contains(err.Error(), "unordered") || !strings.Contains(err.Error(), "SPARKWING_INSTALL_ALLOW_DOWNGRADE=1") {
			t.Fatalf("equal-time failure=%v, want unordered recovery guidance", err)
		}
	}
	if failures != 1 {
		t.Fatalf("failures=%d want exactly one unordered contender", failures)
	}
	installed, err := readBuildIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Revision != "revision-a" && installed.Revision != "revision-b" {
		t.Fatalf("installed revision=%q want one coordinated contender", installed.Revision)
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

func TestInstallLock_RecoversAbandonedFileWithoutDeletingIt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sparkwing")
	lockPath := target + ".install.lock"
	if err := os.WriteFile(lockPath, []byte("pid=gone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if err := withInstallLock(target, time.Second, func() error {
		called = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("guarded operation was not called for abandoned lock file")
	}
	after, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("abandoned lock file was deleted or replaced")
	}
}

func TestInstallLock_AgedLiveOwnerIsNeverDeletedOrReplaced(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sparkwing")
	lockPath := target + ".install.lock"
	owner, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	locked, err := tryInstallLock(owner)
	if err != nil || !locked {
		t.Fatalf("take owner lock: locked=%t err=%v", locked, err)
	}
	defer unlockInstallLock(owner)
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	err = withInstallLock(target, 80*time.Millisecond, func() error {
		t.Fatal("contender entered while aged owner was live")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "timed out waiting") {
		t.Fatalf("contender error=%v want timeout", err)
	}
	after, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("aged live owner's lock was deleted or replaced")
	}
}

func TestInstallLock_ReplacedLiveOwnerIsNeverDeleted(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sparkwing")
	lockPath := target + ".install.lock"
	if err := os.WriteFile(lockPath, []byte("abandoned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "replacement-lock")
	if err := os.WriteFile(replacement, []byte("replacement owner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, lockPath); err != nil {
		t.Fatal(err)
	}
	owner, err := os.OpenFile(lockPath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	locked, err := tryInstallLock(owner)
	if err != nil || !locked {
		t.Fatalf("take replacement owner lock: locked=%t err=%v", locked, err)
	}
	defer unlockInstallLock(owner)
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	err = withInstallLock(target, 80*time.Millisecond, func() error {
		t.Fatal("contender entered while replacement owner was live")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "timed out waiting") {
		t.Fatalf("contender error=%v want timeout", err)
	}
	after, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("replacement live owner's lock was deleted or replaced")
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
