package fleet

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParentGuardCancelsChildWhenCoordinatorProcessDisappears(t *testing.T) {
	guard, err := StartParentGuard()
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop, err := JoinParentGuard(context.Background(), guard.Address, guard.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	guard.Close()
	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), ErrCoordinatorProcessGone) {
			t.Fatalf("child cancellation cause = %v", context.Cause(ctx))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("child did not observe coordinator process loss")
	}
}

func TestParentGuardRejectsWrongOrDuplicateChildWithoutExposingCredential(t *testing.T) {
	guard, err := StartParentGuard()
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if _, _, err := JoinParentGuard(context.Background(), guard.Address, "wrong-"+guard.Token); err == nil || strings.Contains(err.Error(), guard.Token) {
		t.Fatalf("wrong-credential error = %v", err)
	}
	_, stop, err := JoinParentGuard(context.Background(), guard.Address, guard.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if _, _, err := JoinParentGuard(context.Background(), guard.Address, guard.Token); err == nil || !strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), guard.Token) {
		t.Fatalf("duplicate-child error = %v", err)
	}
}

func TestParentGuardNeverSendsCredentialToANonLoopbackAddress(t *testing.T) {
	const token = "private-lifetime-token"
	for _, address := range []string{
		"https://example.test:443",
		"http://100.64.1.2:4346",
		"http://user@example.test:80",
		"http://localhost:4346",
	} {
		if _, _, err := JoinParentGuard(context.Background(), address, token); err == nil || !strings.Contains(err.Error(), "loopback HTTP origin") || strings.Contains(err.Error(), token) {
			t.Fatalf("address %q error = %v", address, err)
		}
	}
}

func TestParentGuardProcessDeathCancelsIndependentChild(t *testing.T) {
	mode := os.Getenv("SPARKWING_TEST_PARENT_GUARD_MODE")
	ready := os.Getenv("SPARKWING_TEST_PARENT_GUARD_READY")
	observed := os.Getenv("SPARKWING_TEST_PARENT_GUARD_OBSERVED")
	pidPath := os.Getenv("SPARKWING_TEST_PARENT_GUARD_CHILD_PID")
	switch mode {
	case "parent":
		guard, err := StartParentGuard()
		if err != nil {
			os.Exit(20)
		}
		child := exec.Command(os.Args[0], "-test.run=^TestParentGuardProcessDeathCancelsIndependentChild$")
		child.Env = parentGuardTestEnv(os.Environ(), map[string]string{
			"SPARKWING_TEST_PARENT_GUARD_MODE":    "child",
			"SPARKWING_TEST_PARENT_GUARD_ADDRESS": guard.Address,
			"SPARKWING_TEST_PARENT_GUARD_TOKEN":   guard.Token,
		})
		if err := child.Start(); err != nil {
			os.Exit(21)
		}
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			_ = child.Process.Kill()
			os.Exit(22)
		}
		select {}
	case "child":
		ctx, stop, err := JoinParentGuard(context.Background(),
			os.Getenv("SPARKWING_TEST_PARENT_GUARD_ADDRESS"), os.Getenv("SPARKWING_TEST_PARENT_GUARD_TOKEN"))
		if err != nil {
			os.Exit(23)
		}
		defer stop()
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
			os.Exit(24)
		}
		<-ctx.Done()
		if !errors.Is(context.Cause(ctx), ErrCoordinatorProcessGone) {
			os.Exit(25)
		}
		if err := os.WriteFile(observed, []byte("coordinator-gone"), 0o600); err != nil {
			os.Exit(26)
		}
		return
	}

	dir := t.TempDir()
	ready = filepath.Join(dir, "child-ready")
	observed = filepath.Join(dir, "child-observed")
	pidPath = filepath.Join(dir, "child-pid")
	parent := exec.Command(os.Args[0], "-test.run=^TestParentGuardProcessDeathCancelsIndependentChild$")
	parent.Env = parentGuardTestEnv(os.Environ(), map[string]string{
		"SPARKWING_TEST_PARENT_GUARD_MODE":      "parent",
		"SPARKWING_TEST_PARENT_GUARD_READY":     ready,
		"SPARKWING_TEST_PARENT_GUARD_OBSERVED":  observed,
		"SPARKWING_TEST_PARENT_GUARD_CHILD_PID": pidPath,
	})
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Process.Kill() }()
	if !waitForParentGuardTestFile(ready, 10*time.Second) {
		t.Fatal("coordinator child did not attach to its parent lifetime")
	}
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = parent.Wait()
	if !waitForParentGuardTestFile(observed, 5*time.Second) {
		if body, err := os.ReadFile(pidPath); err == nil {
			if pid, err := strconv.Atoi(string(body)); err == nil {
				if child, err := os.FindProcess(pid); err == nil {
					_ = child.Kill()
				}
			}
		}
		t.Fatal("coordinator child did not observe hard parent-process death")
	}
}

func parentGuardTestEnv(base []string, overrides map[string]string) []string {
	out := append([]string(nil), base...)
	for key, value := range overrides {
		prefix := key + "="
		filtered := out[:0]
		for _, entry := range out {
			if !strings.HasPrefix(entry, prefix) {
				filtered = append(filtered, entry)
			}
		}
		out = append(filtered, prefix+value)
	}
	return out
}

func waitForParentGuardTestFile(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
