//go:build unix

package jobs

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReleaseLintWaitsForAnotherTree(t *testing.T) {
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock is unavailable")
	}
	repo := t.TempDir()
	t.Chdir(repo)
	lock, err := os.Create(filepath.Join(repo, "lint.lock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(repo, "waiting")
	if err := unix.Mkfifo(readyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	ready, err := os.OpenFile(readyPath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ready.Close() })
	t.Setenv("RELEASE_TEST_LOCK", lock.Name())
	t.Setenv("RELEASE_TEST_READY", readyPath)
	bin := filepath.Join(repo, "bin")
	for name, body := range map[string]string{
		"git": `case "$*" in *ls-files*) echo go.mod ;; *rev-parse*) echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa ;; *) exit 1 ;; esac`,
		"go":  "echo fixture",
		"golangci-lint": `exec 9>"$RELEASE_TEST_LOCK"
if ! flock -n 9; then
  case "$*" in *--allow-serial-runners*) ;; *) exit 3 ;; esac
  echo waiting > "$RELEASE_TEST_READY"
  flock 9
fi`,
	} {
		path := filepath.Join(bin, name)
		writeGoFile(t, path, "#!/bin/sh\n"+body+"\n")
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	host, plan, _ := prepareWork(t, &releaseCutChecksJob{})
	t.Cleanup(func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) })
	done := make(chan error, 1)
	go func() {
		_, err := host.RunStep(t.Context(), plan, "job", "lint")
		done <- err
	}()
	waiting := make(chan error, 1)
	go func() {
		_, err := bufio.NewReader(ready).ReadString('\n')
		waiting <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("lint finished while another tree held the lock: %v", err)
	case err := <-waiting:
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("lint did not run after the lock was released: %v", err)
	}
	var logs strings.Builder
	for _, record := range host.Records() {
		logs.WriteString(record.Msg)
	}
	if !strings.Contains(logs.String(), "waiting for the box-wide lock") || !strings.Contains(logs.String(), "deadline 40m0s") {
		t.Fatalf("release lint lost its waiting message or long deadline:\n%s", logs.String())
	}
	if got := mustNode(t, releasePlan(t), "release-cut-checks").TimeoutMS; got < (40 * time.Minute).Milliseconds() {
		t.Fatalf("release job timeout %dms cannot cover the lock wait and checks", got)
	}
}
