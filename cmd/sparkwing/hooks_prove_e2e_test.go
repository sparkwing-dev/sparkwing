//go:build e2e

package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHooksInstall_PriorHookStaysLiveUntilProofPasses(t *testing.T) {
	f := newChainFixture(t)
	hooks := filepath.Join(f.repo, ".git", "hooks")
	priorPath := filepath.Join(hooks, "pre-commit")
	priorBody := renderHookScript("pre-commit", []string{"old-gate"}, false, "")
	writeExec(t, priorPath, priorBody)
	f.git(t, "config", "core.hooksPath", hooks)
	priorConfig := f.git(t, "config", "--local", "core.hooksPath")

	proofStarted := make(chan struct{})
	releaseProof := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"), installOptions{
			prove: func(_, _ string) error {
				close(proofStarted)
				<-releaseProof
				return nil
			},
		})
		done <- err
	}()
	select {
	case <-proofStarted:
	case <-time.After(time.Second):
		t.Fatal("installer did not begin the blocking proof")
	}
	if got := readRepoFile(t, priorPath); got != priorBody {
		t.Fatal("candidate hook was published while its proof was blocked")
	}
	if got := f.git(t, "config", "--local", "core.hooksPath"); got != priorConfig {
		t.Fatalf("core.hooksPath changed during proof from %q to %q", priorConfig, got)
	}
	f.git(t, "add", "-A")
	f.git(t, "commit", "-m", "invoke prior hook during proof")
	if ran := f.ranPipelines(t); !strings.Contains(ran, "run old-gate --sw-local-only") || strings.Contains(ran, "run gate --sw-local-only") {
		t.Fatalf("commit during proof did not run only the prior hook: %q", ran)
	}

	close(releaseProof)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("install after proof: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("installer did not publish after proof passed")
	}
	after := readRepoFile(t, priorPath)
	if after == priorBody || !strings.Contains(after, "sparkwing run 'gate'") {
		t.Fatalf("passed proof did not atomically publish the replacement:\n%s", after)
	}
	if got := f.git(t, "config", "--local", "core.hooksPath"); got != priorConfig {
		t.Fatalf("replacement changed core.hooksPath from %q to %q", priorConfig, got)
	}
	writeRepoFile(t, filepath.Join(f.repo, "after-proof"), "published\n")
	f.git(t, "add", "-A")
	f.git(t, "commit", "-m", "invoke replacement hook")
	if ran := f.ranPipelines(t); !strings.Contains(ran, "run gate --sw-local-only") {
		t.Fatalf("published replacement hook did not fire: %q", ran)
	}
}
