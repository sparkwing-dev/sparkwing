package boxslot_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/boxslot"
)

func TestAnnotateHolder(t *testing.T) {
	cases := []struct {
		name    string
		acquire bool
		wantErr bool
	}{
		{"appends run line to own holder", true, false},
		{"errors when this pid holds no slot", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.acquire {
				release, err := boxslot.Acquire(context.Background(), boxslot.Options{
					MaxSlots: 1,
					LockDir:  dir,
				})
				if err != nil {
					t.Fatalf("Acquire: %v", err)
				}
				defer release()
			}

			err := boxslot.AnnotateHolder(dir, "run-20260701-000000-deadbeef")

			if tc.wantErr {
				if err == nil {
					t.Fatal("AnnotateHolder without a holder: want error, got nil")
				}
				entries, readErr := os.ReadDir(dir)
				if readErr != nil {
					t.Fatalf("ReadDir: %v", readErr)
				}
				if len(entries) != 0 {
					t.Fatalf("AnnotateHolder failure left %d files behind", len(entries))
				}
				return
			}
			if err != nil {
				t.Fatalf("AnnotateHolder: %v", err)
			}

			content := readOwnHolder(t, dir)
			lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
			if len(lines) != 2 {
				t.Fatalf("holder file has %d lines, want 2:\n%s", len(lines), content)
			}
			if !strings.HasPrefix(lines[0], "pid=") || !strings.Contains(lines[0], " start=") {
				t.Errorf("line 1 = %q, want pid=<pid> start=<rfc3339>", lines[0])
			}
			if lines[1] != "run=run-20260701-000000-deadbeef" {
				t.Errorf("line 2 = %q, want run=run-20260701-000000-deadbeef", lines[1])
			}
		})
	}
}

func TestHolders_ListsLiveAndStaleWithParsedMetadata(t *testing.T) {
	dir := t.TempDir()
	release, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 2,
		LockDir:  dir,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()
	if err := boxslot.AnnotateHolder(dir, "run-20260701-000000-beef0002"); err != nil {
		t.Fatalf("AnnotateHolder: %v", err)
	}

	staleName := "holder-pid99999-1700000000000000000-1.lock"
	staleBody := "pid=99999 start=2026-01-01T00:00:00Z\nrun=run-20260101-000000-cafe0001\n"
	if err := os.WriteFile(filepath.Join(dir, staleName), []byte(staleBody), 0o600); err != nil {
		t.Fatalf("seed stale holder: %v", err)
	}

	holders, err := boxslot.Holders(dir)
	if err != nil {
		t.Fatalf("Holders: %v", err)
	}
	if len(holders) != 2 {
		t.Fatalf("Holders returned %d rows, want 2: %+v", len(holders), holders)
	}

	stale := holders[0]
	if stale.PID != 99999 {
		t.Errorf("stale PID = %d, want 99999", stale.PID)
	}
	if got, want := stale.ClaimedAt.UnixNano(), int64(1700000000000000000); got != want {
		t.Errorf("stale ClaimedAt = %d, want %d", got, want)
	}
	if stale.RunID != "run-20260101-000000-cafe0001" {
		t.Errorf("stale RunID = %q, want run-20260101-000000-cafe0001", stale.RunID)
	}
	if stale.Live {
		t.Error("stale holder reported live")
	}
	if filepath.Base(stale.Path) != staleName {
		t.Errorf("stale Path = %q, want basename %q", stale.Path, staleName)
	}

	live := holders[1]
	if live.PID != os.Getpid() {
		t.Errorf("live PID = %d, want %d", live.PID, os.Getpid())
	}
	if live.RunID != "run-20260701-000000-beef0002" {
		t.Errorf("live RunID = %q, want run-20260701-000000-beef0002", live.RunID)
	}
	if !live.Live {
		t.Error("own holder reported stale")
	}
}

func TestHolders_AbsentLockDirReportsNone(t *testing.T) {
	holders, err := boxslot.Holders(filepath.Join(t.TempDir(), "never-created"))
	if err != nil {
		t.Fatalf("Holders on absent dir: %v", err)
	}
	if len(holders) != 0 {
		t.Fatalf("Holders on absent dir = %+v, want none", holders)
	}
}

func TestReleaseHolder_RemovesStaleFile(t *testing.T) {
	dir := t.TempDir()
	name := "holder-pid99999-1700000000000000000-1.lock"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("pid=99999 start=2026-01-01T00:00:00Z\n"), 0o600); err != nil {
		t.Fatalf("seed stale holder: %v", err)
	}
	if err := boxslot.ReleaseHolder(dir, name, false); err != nil {
		t.Fatalf("ReleaseHolder stale: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale holder still present: %v", err)
	}
}

func TestReleaseHolder_RefusesLiveWithoutForce(t *testing.T) {
	dir := t.TempDir()
	release, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1,
		LockDir:  dir,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()

	name := ownHolderName(t, dir)
	err = boxslot.ReleaseHolder(dir, name, false)
	if !errors.Is(err, boxslot.ErrHolderLive) {
		t.Fatalf("ReleaseHolder on live holder err = %v, want ErrHolderLive", err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Fatalf("live holder was removed despite refusal: %v", err)
	}
}

func TestReleaseHolder_RejectsNonHolderNames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"coord.lock", "../holder-pid1-0-0.lock", "holder-pid1-0-0", "cap.control"} {
		if err := boxslot.ReleaseHolder(dir, name, false); err == nil {
			t.Errorf("ReleaseHolder(%q) = nil, want error", name)
		}
	}
}

func TestReleaseHolder_ForceKillsLiveChildOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGKILL semantics differ on windows; covered by the stale-file test")
	}
	if childDir := os.Getenv("BOXSLOT_RELEASE_CHILD_LOCK_DIR"); childDir != "" {
		release, err := boxslot.Acquire(context.Background(), boxslot.Options{
			MaxSlots: 1,
			LockDir:  childDir,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "child: Acquire: %v\n", err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stdout, "READY")
		_ = release
		select {}
	}

	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^TestReleaseHolder_ForceKillsLiveChildOwner$", "-test.v")
	cmd.Env = append(os.Environ(), "BOXSLOT_RELEASE_CHILD_LOCK_DIR="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	ready := make(chan struct{})
	go func() {
		buf := make([]byte, 512)
		for {
			n, err := stdout.Read(buf)
			if n > 0 && strings.Contains(string(buf[:n]), "READY") {
				close(ready)
				return
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("child never signaled READY")
	}

	holders, err := boxslot.Holders(dir)
	if err != nil {
		t.Fatalf("Holders: %v", err)
	}
	if len(holders) != 1 || !holders[0].Live || holders[0].PID != cmd.Process.Pid {
		t.Fatalf("pre-release holders = %+v, want one live row for pid %d", holders, cmd.Process.Pid)
	}
	name := filepath.Base(holders[0].Path)

	if err := boxslot.ReleaseHolder(dir, name, true); err != nil {
		t.Fatalf("ReleaseHolder --force: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("holder file survived force release: %v", err)
	}
	_ = cmd.Wait()

	release, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1, LockDir: dir, NoWait: true,
	})
	if err != nil {
		t.Fatalf("Acquire after force release: %v", err)
	}
	release()
}

func ownHolderName(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	prefix := fmt.Sprintf("holder-pid%d-", os.Getpid())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			return e.Name()
		}
	}
	t.Fatal("no holder file for this pid")
	return ""
}

func readOwnHolder(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "holder-") {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("read holder: %v", err)
			}
			return string(b)
		}
	}
	t.Fatal("no holder file found")
	return ""
}
