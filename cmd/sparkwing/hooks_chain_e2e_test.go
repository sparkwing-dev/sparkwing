//go:build e2e

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
)

func TestHooksInstall_GateAndGlobalHookBothFireUnderAGlobalHooksPath(t *testing.T) {
	f := newChainFixture(t)

	captureStdout(t, func() {
		if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"), installOptions{}); err != nil {
			t.Fatalf("install: %v", err)
		}
	})

	f.git(t, "add", "-A")
	f.git(t, "commit", "-m", "first")

	if ran := f.ranPipelines(t); !strings.Contains(ran, "run gate") {
		t.Errorf("the pre-commit gate never ran under a global core.hooksPath; sparkwing was asked for: %q", ran)
	}
	if _, err := os.Stat(f.sentinelOf("pre-commit")); err != nil {
		t.Error("the global pre-commit hook was dropped instead of chained after the gate")
	}
	if _, err := os.Stat(f.sentinelOf("prepare-commit-msg")); err != nil {
		t.Error("the global prepare-commit-msg hook stopped firing once the repo claimed core.hooksPath")
	}
}

func TestHooksInstall_KeepsTheGlobalHookFiringWhenItCannotForwardIt(t *testing.T) {
	f := newChainFixture(t)
	hooksDir, err := githooks.Dir(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	writeExec(t, filepath.Join(hooksDir, "prepare-commit-msg"),
		"#!/bin/sh\n: > "+f.sentinelOf("repo-prepare")+"\nexit 0\n")

	out := captureStderr(t, func() {
		if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"), installOptions{}); err == nil {
			t.Fatal("rejected installation returned success")
		}
	})

	f.git(t, "add", "-A")
	f.git(t, "commit", "-m", "first")

	if _, err := os.Stat(f.sentinelOf("prepare-commit-msg")); err != nil {
		t.Error("the machine's global prepare-commit-msg stopped firing after an install that could not forward it")
	}
	if !strings.Contains(out, "core.hooksPath left alone") || !strings.Contains(out, "prepare-commit-msg") {
		t.Errorf("install should say which global hook held the claim back:\n%s", out)
	}
}

func TestHooksInstall_FailingGateAbortsTheCommit(t *testing.T) {
	f := newChainFixture(t)
	writeExec(t, filepath.Join(f.binDir, "sparkwing"),
		"#!/bin/sh\necho \"$@\" >> "+f.ranFile+"\nexit 1\n")

	captureStdout(t, func() {
		if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"), installOptions{}); err != nil {
			t.Fatalf("install: %v", err)
		}
	})

	f.git(t, "add", "-A")
	if out, err := f.tryGit(f.repo, "commit", "-m", "first"); err == nil {
		t.Fatalf("a failing gate let the commit through:\n%s", out)
	}
	if _, err := os.Stat(f.sentinelOf("pre-commit")); err == nil {
		t.Error("the chain ran the global hook after the gate already failed")
	}
}

func TestHooksInstall_FailingPrePushAbortsThePush(t *testing.T) {
	f := newChainFixture(t)
	writeRepoFile(t, filepath.Join(f.repo, ".sparkwing", "sparkwing.yaml"), `pipelines:
  - name: push-gate
    entrypoint: PushGate
    on:
      pre_push: {}
`)
	f.git(t, "add", "-A")
	f.git(t, "commit", "-m", "initial")
	remote := filepath.Join(f.root, "remote.git")
	f.git(t, "init", "--bare", remote)
	f.git(t, "remote", "add", "origin", remote)
	writeExec(t, filepath.Join(f.binDir, "sparkwing"),
		"#!/bin/sh\necho \"$@\" >> "+f.ranFile+"\nexit 1\n")

	captureStdout(t, func() {
		if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"), installOptions{}); err != nil {
			t.Fatalf("install: %v", err)
		}
	})
	if out, err := f.tryGit(f.repo, "push", "-u", "origin", "main"); err == nil {
		t.Fatalf("a failing declared pre-push let the push through:\n%s", out)
	}
	if _, err := f.tryGit(remote, "rev-parse", "refs/heads/main"); err == nil {
		t.Fatal("the rejected push updated the remote branch")
	}
	if ran := f.ranPipelines(t); !strings.Contains(ran, "run push-gate") {
		t.Fatalf("pre-push did not invoke its declared pipeline: %q", ran)
	}

	writeExec(t, filepath.Join(f.binDir, "sparkwing"), "#!/bin/sh\nexit 0\n")
	f.git(t, "push", "-u", "origin", "main")
	if _, err := f.tryGit(remote, "rev-parse", "refs/heads/main"); err != nil {
		t.Fatalf("the passing control did not update the remote: %v", err)
	}
}

func TestHooksStatus_LocalShadowRemedyReachesFiringHooks(t *testing.T) {
	f := newChainFixture(t)
	shadowDir := filepath.Join(f.root, "local-hooks")
	if err := os.MkdirAll(shadowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f.git(t, "config", "core.hooksPath", shadowDir)
	captureStdout(t, func() {
		if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"), installOptions{}); err == nil {
			t.Fatal("rejected installation returned success")
		}
	})

	out := captureStdout(t, func() {
		if err := statusHooks(f.tryGit, f.repo, "pretty"); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	wantUnset := "git -C " + f.repo + " config --unset core.hooksPath"
	wantInstall := "sparkwing pipeline hooks install --repo " + f.repo
	if !strings.Contains(out, wantUnset) || !strings.Contains(out, wantInstall) {
		t.Fatalf("status remedy did not name both required steps:\n%s", out)
	}

	f.git(t, "config", "--unset", "core.hooksPath")
	captureStdout(t, func() {
		if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"), installOptions{}); err != nil {
			t.Fatalf("remedial install: %v", err)
		}
	})
	declared, err := declaredHookNames(f.repo)
	if err != nil {
		t.Fatalf("declaredHookNames: %v", err)
	}
	if survey := githooks.Survey(f.tryGit, f.repo, declared); survey.State != githooks.GateArmed {
		t.Fatalf("advertised remedy left hooks unfired: %+v", survey)
	}
	f.git(t, "add", "-A")
	f.git(t, "commit", "-m", "prove advertised remedy")
	if ran := f.ranPipelines(t); !strings.Contains(ran, "run gate") {
		t.Fatalf("advertised remedy did not produce a firing hook: %q", ran)
	}
}
