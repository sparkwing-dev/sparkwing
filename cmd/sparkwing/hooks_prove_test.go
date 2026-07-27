package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// localHooksPath is the claim an install makes so git reads the repo's own
// hooks instead of the machine's; empty means the claim was not made.
func localHooksPath(t *testing.T, f *chainFixture) string {
	t.Helper()
	out, err := f.tryGit(f.repo, "config", "--local", "--type=path", "core.hooksPath")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// recordingProver answers every proof with err and remembers what it was
// asked to run.
func recordingProver(err error, seen *[]string) Prover {
	return func(_, pipeline string) error {
		*seen = append(*seen, pipeline)
		return err
	}
}

// twoGateProject declares a commit gate and a push gate, so a test can fail
// one proof and watch what happens to the other.
const twoGateProject = `pipelines:
  - name: gate
    entrypoint: Gate
    on:
      pre_commit: {}
  - name: push-gate
    entrypoint: PushGate
    on:
      pre_push: {}
`

// gateAndNotifyProject declares a commit gate and a post-commit notifier, so
// a test can fail every proof there is to fail and still leave a hook the
// install never proved.
const gateAndNotifyProject = `pipelines:
  - name: gate
    entrypoint: Gate
    on:
      pre_commit: {}
  - name: notify
    entrypoint: Notify
    on:
      post_commit: {}
`

// notifyOnlyProject declares a hook that fires after the commit has landed
// and nothing that can refuse one.
const notifyOnlyProject = `pipelines:
  - name: notify
    entrypoint: Notify
    on:
      post_commit: {}
`

// redPushGate passes every proof but the push gate's.
func redPushGate(_, pipeline string) error {
	if pipeline == "push-gate" {
		return errors.New("push gate is red")
	}
	return nil
}

func TestHooksInstall_ClaimsTheHooksPathOnceTheGateProves(t *testing.T) {
	f := newChainFixture(t)
	var proved []string
	captureStdout(t, func() {
		if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"),
			installOptions{prove: recordingProver(nil, &proved)}); err != nil {
			t.Fatalf("installHooks: %v", err)
		}
	})
	if len(proved) != 1 || proved[0] != "gate" {
		t.Errorf("proved = %v, want [gate]", proved)
	}
	want := filepath.Join(f.repo, ".git", "hooks")
	if got := localHooksPath(t, f); got != want {
		t.Errorf("core.hooksPath = %q, want %q", got, want)
	}
}

func TestHooksInstall_LeavesTheHooksPathAloneWhenNoGatePasses(t *testing.T) {
	f := newChainFixture(t)
	var proved []string
	var gated bool
	out := captureStdout(t, func() {
		var err error
		gated, err = installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"),
			installOptions{prove: recordingProver(errors.New("admission unreachable"), &proved)})
		if err != nil {
			t.Fatalf("installHooks: %v", err)
		}
	})
	if gated {
		t.Error("installHooks reported the repo gated after every proof failed")
	}
	if got := localHooksPath(t, f); got != "" {
		t.Errorf("core.hooksPath = %q, want it unclaimed: arming a gate that cannot run blocks every commit", got)
	}
	for _, want := range []string{"admission unreachable", "--no-prove"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want it to mention %q", out, want)
		}
	}
}

// Hooks git was never going to read are inert whether or not this run leaves
// them there, so deleting them would be the only lasting thing it did.
func TestHooksInstall_KeepsTheHooksOfARepoItCannotArm(t *testing.T) {
	f := newChainFixture(t)
	prior := filepath.Join(f.repo, ".git", "hooks", "pre-commit")
	writeExec(t, prior, renderHookScript("pre-commit", []string{"gate"}, false))

	var proved []string
	captureStdout(t, func() {
		if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"),
			installOptions{prove: recordingProver(errors.New("admission unreachable"), &proved)}); err != nil {
			t.Fatalf("installHooks: %v", err)
		}
	})
	if _, err := os.Stat(prior); err != nil {
		t.Errorf("an install that armed nothing deleted the shadowed hook it found: %v", err)
	}
}

// A post-commit hook is never proven, so counting it among the hooks left to
// arm hands a repo whose only gate is red the arming path: the claim it must
// not make, and the deletion of the hook it was not going to fire.
func TestHooksInstall_ArmsNothingWhenTheGateIsRedAndAPostCommitHookIsDeclared(t *testing.T) {
	f := newChainFixture(t)
	writeRepoFile(t, filepath.Join(f.repo, ".sparkwing", "sparkwing.yaml"), gateAndNotifyProject)
	prior := filepath.Join(f.repo, ".git", "hooks", "pre-commit")
	writeExec(t, prior, renderHookScript("pre-commit", []string{"gate"}, false))

	var proved []string
	var gated bool
	captureStdout(t, func() {
		var err error
		gated, err = installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"),
			installOptions{prove: recordingProver(errors.New("admission unreachable"), &proved)})
		if err != nil {
			t.Fatalf("installHooks: %v", err)
		}
	})
	if len(proved) != 1 || proved[0] != "gate" {
		t.Errorf("proved = %v, want [gate]: only a blocking hook is worth a run", proved)
	}
	if gated {
		t.Error("installHooks reported the repo gated with the only gate it declares red")
	}
	if got := localHooksPath(t, f); got != "" {
		t.Errorf("core.hooksPath = %q, want it unclaimed: arming a gate that cannot run blocks every commit", got)
	}
	if _, err := os.Stat(prior); err != nil {
		t.Errorf("an install that armed nothing deleted the shadowed hook it found: %v", err)
	}
}

// A repo whose only hook is post-commit gates nothing, so the install arms
// the notifier and still reports commits going through unchecked.
func TestHooksInstall_ReportsARepoWithOnlyAPostCommitHookUngated(t *testing.T) {
	f := newChainFixture(t)
	writeRepoFile(t, filepath.Join(f.repo, ".sparkwing", "sparkwing.yaml"), notifyOnlyProject)
	var gated bool
	captureStdout(t, func() {
		var err error
		if gated, err = installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"), installOptions{}); err != nil {
			t.Fatalf("installHooks: %v", err)
		}
	})
	if gated {
		t.Error("installHooks reported a repo gated whose only hook runs after the commit has landed")
	}
	if got := localHooksPath(t, f); got == "" {
		t.Error("core.hooksPath unclaimed, want the post-commit hook armed even though it gates nothing")
	}
}

// A repo git already reads has no claim left to hold an unproven hook back,
// so the gate a re-install rewrites has to prove itself before it goes live.
func TestHooksInstall_RemovesTheLiveGateThatStoppedPassing(t *testing.T) {
	f := newChainFixture(t)
	captureStdout(t, func() {
		if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"), installOptions{}); err != nil {
			t.Fatalf("installHooks: %v", err)
		}
	})
	var proved []string
	var gated bool
	out := captureStdout(t, func() {
		var err error
		gated, err = installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"),
			installOptions{prove: recordingProver(errors.New("gate went red"), &proved)})
		if err != nil {
			t.Fatalf("installHooks: %v", err)
		}
	})
	if len(proved) != 1 || proved[0] != "gate" {
		t.Errorf("proved = %v, want the gate proven again on a repo git already reads", proved)
	}
	if _, err := os.Stat(filepath.Join(f.repo, ".git", "hooks", "pre-commit")); !os.IsNotExist(err) {
		t.Errorf("a gate that stopped passing stayed live after a re-install (stat err = %v)", err)
	}
	if gated {
		t.Error("installHooks reported the repo gated with its only gate withdrawn")
	}
	if !strings.Contains(out, "gate went red") {
		t.Errorf("output = %q, want the failure that withdrew the gate", out)
	}
}

// Withdrawal has to survive the next install: rewriting every declared hook
// is how a gate an earlier run took out comes back unproven.
func TestHooksInstall_DoesNotRestoreAWithdrawnGateOnAReinstall(t *testing.T) {
	f := newChainFixture(t)
	writeRepoFile(t, filepath.Join(f.repo, ".sparkwing", "sparkwing.yaml"), twoGateProject)
	install := func() string {
		return captureStdout(t, func() {
			if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"),
				installOptions{prove: redPushGate}); err != nil {
				t.Fatalf("installHooks: %v", err)
			}
		})
	}
	install()
	out := install()

	hooks := filepath.Join(f.repo, ".git", "hooks")
	if _, err := os.Stat(filepath.Join(hooks, "pre-push")); !os.IsNotExist(err) {
		t.Errorf("the re-install put the withdrawn pre-push back (stat err = %v)", err)
	}
	if body := readRepoFile(t, filepath.Join(hooks, "pre-commit")); !strings.Contains(body, "sparkwing run gate") {
		t.Errorf("pre-commit hook = %q, want the passing gate still armed", body)
	}
	if !strings.Contains(out, "pre-push withdrawn") {
		t.Errorf("output = %q, want the second install to name the withdrawal too", out)
	}
}

// A red push gate is not a reason to leave a working commit gate unarmed.
func TestHooksInstall_ArmsThePassingGateAndWithdrawsOnlyTheFailingOne(t *testing.T) {
	f := newChainFixture(t)
	writeRepoFile(t, filepath.Join(f.repo, ".sparkwing", "sparkwing.yaml"), twoGateProject)
	out := captureStdout(t, func() {
		if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"),
			installOptions{prove: redPushGate}); err != nil {
			t.Fatalf("installHooks: %v", err)
		}
	})
	hooks := filepath.Join(f.repo, ".git", "hooks")
	if _, err := os.Stat(filepath.Join(hooks, "pre-push")); !os.IsNotExist(err) {
		t.Errorf("pre-push survived a failed proof (stat err = %v)", err)
	}
	if body := readRepoFile(t, filepath.Join(hooks, "pre-commit")); !strings.Contains(body, "sparkwing run gate") {
		t.Errorf("pre-commit hook = %q, want the passing gate kept", body)
	}
	if got := localHooksPath(t, f); got != hooks {
		t.Errorf("core.hooksPath = %q, want %q: the commit gate proved and should be armed", got, hooks)
	}
	if !strings.Contains(out, "pre-push withdrawn") {
		t.Errorf("output = %q, want it to name the withdrawn hook", out)
	}
}

func TestHooksInstall_ProvesBlockingGatesAndNotThePostCommitOne(t *testing.T) {
	f := newChainFixture(t)
	writeRepoFile(t, filepath.Join(f.repo, ".sparkwing", "sparkwing.yaml"), `pipelines:
  - name: gate
    entrypoint: Gate
    on:
      pre_commit: {}
  - name: push-gate
    entrypoint: PushGate
    on:
      pre_push: {}
  - name: notify
    entrypoint: Notify
    on:
      post_commit: {}
`)
	var proved []string
	captureStdout(t, func() {
		if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"),
			installOptions{prove: recordingProver(nil, &proved)}); err != nil {
			t.Fatalf("installHooks: %v", err)
		}
	})
	got := strings.Join(proved, ",")
	if got != "gate,push-gate" {
		t.Errorf("proved = %q, want %q: post-commit cannot abort a commit, so it is not worth a run", got, "gate,push-gate")
	}
}

// Install refuses to overwrite a hand-written hook, so withdrawal must not
// delete one either -- the file belongs to the operator, and a failing proof
// says nothing about it.
func TestHooksInstall_LeavesAHandWrittenHookAloneWhenItsGateDoesNotPass(t *testing.T) {
	f := newChainFixture(t)
	writeRepoFile(t, filepath.Join(f.repo, ".sparkwing", "sparkwing.yaml"), twoGateProject)
	handWritten := "#!/bin/sh\n# mine, not sparkwing's\nexit 0\n"
	prePush := filepath.Join(f.repo, ".git", "hooks", "pre-push")
	writeExec(t, prePush, handWritten)

	out := captureStdout(t, func() {
		if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"),
			installOptions{prove: redPushGate}); err != nil {
			t.Fatalf("installHooks: %v", err)
		}
	})
	if got := readRepoFile(t, prePush); got != handWritten {
		t.Errorf("hand-written pre-push = %q, want it untouched (%q)", got, handWritten)
	}
	if !strings.Contains(out, "not sparkwing's to remove") {
		t.Errorf("output = %q, want it to say the hook was left alone", out)
	}
}

func TestHooksInstall_ArmsWithoutProvingWhenNoProverIsSupplied(t *testing.T) {
	f := newChainFixture(t)
	var gated bool
	captureStdout(t, func() {
		var err error
		if gated, err = installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"), installOptions{}); err != nil {
			t.Fatalf("installHooks: %v", err)
		}
	})
	if !gated {
		t.Error("installHooks reported the repo ungated after claiming the hooks path")
	}
	if got := localHooksPath(t, f); got == "" {
		t.Error("core.hooksPath unclaimed, want the claim when the operator opted out of the proof")
	}
}

func TestDeclaredHookNames_ReadsTheTriggersThePipelinesDeclare(t *testing.T) {
	f := newChainFixture(t)
	writeRepoFile(t, filepath.Join(f.repo, ".sparkwing", "sparkwing.yaml"), `pipelines:
  - name: gate
    entrypoint: Gate
    on:
      pre_commit: {}
      pre_push: {}
`)
	got := strings.Join(declaredHookNames(f.repo), ",")
	if got != "pre-commit,pre-push" {
		t.Errorf("declaredHookNames = %q, want %q", got, "pre-commit,pre-push")
	}
}

func TestDeclaredHookNames_UnreadableProjectDeclaresNothing(t *testing.T) {
	if got := declaredHookNames(t.TempDir()); got != nil {
		t.Errorf("declaredHookNames = %v, want nil for a directory with no project", got)
	}
}
