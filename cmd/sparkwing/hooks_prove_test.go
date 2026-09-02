package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func localHooksPath(t *testing.T, f *chainFixture) string {
	t.Helper()
	out, err := f.tryGit(f.repo, "config", "--local", "--type=path", "core.hooksPath")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func recordingProver(err error, seen *[]string) Prover {
	return func(_, pipeline string) error {
		*seen = append(*seen, pipeline)
		return err
	}
}

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

const notifyOnlyProject = `pipelines:
  - name: notify
    entrypoint: Notify
    on:
      post_commit: {}
`

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

func TestHooksInstall_KeepsTheHooksOfARepoItCannotArm(t *testing.T) {
	f := newChainFixture(t)
	prior := filepath.Join(f.repo, ".git", "hooks", "pre-commit")
	writeExec(t, prior, renderHookScript("pre-commit", []string{"gate"}, false, ""))

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

func TestHooksInstall_ArmsNothingWhenTheGateIsRedAndAPostCommitHookIsDeclared(t *testing.T) {
	f := newChainFixture(t)
	writeRepoFile(t, filepath.Join(f.repo, ".sparkwing", "sparkwing.yaml"), gateAndNotifyProject)
	prior := filepath.Join(f.repo, ".git", "hooks", "pre-commit")
	writeExec(t, prior, renderHookScript("pre-commit", []string{"gate"}, false, ""))

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

func TestHooksInstall_FailedReinstallKeepsThePriorLiveGate(t *testing.T) {
	f := newChainFixture(t)
	captureStdout(t, func() {
		if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"), installOptions{}); err != nil {
			t.Fatalf("installHooks: %v", err)
		}
	})
	path := filepath.Join(f.repo, ".git", "hooks", "pre-commit")
	prior := readRepoFile(t, path)
	priorConfig := f.git(t, "config", "--local", "core.hooksPath")
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
	if got := readRepoFile(t, path); got != prior {
		t.Errorf("failed reinstall changed the prior live gate:\n%s", got)
	}
	if got := f.git(t, "config", "--local", "core.hooksPath"); got != priorConfig {
		t.Errorf("failed reinstall changed core.hooksPath from %q to %q", priorConfig, got)
	}
	if !gated {
		t.Error("installHooks reported the restored live gate as ungated")
	}
	if !strings.Contains(out, "gate went red") || !strings.Contains(out, "remain unchanged") {
		t.Errorf("output = %q, want the proof failure and unchanged prior state", out)
	}
}

func TestHooksInstall_RepeatedFailedProofLeavesAFreshRepoUnchanged(t *testing.T) {
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
	for _, name := range []string{"pre-commit", "pre-push", "prepare-commit-msg"} {
		if _, err := os.Stat(filepath.Join(hooks, name)); !os.IsNotExist(err) {
			t.Errorf("failed proof left %s behind (stat err = %v)", name, err)
		}
	}
	if got := localHooksPath(t, f); got != "" {
		t.Errorf("failed proof left core.hooksPath = %q", got)
	}
	if !strings.Contains(out, "remain unchanged") {
		t.Errorf("output = %q, want the second rejected install to name unchanged prior state", out)
	}
}

func TestHooksInstall_FailedProofDoesNotInstallAPartialGate(t *testing.T) {
	f := newChainFixture(t)
	writeRepoFile(t, filepath.Join(f.repo, ".sparkwing", "sparkwing.yaml"), twoGateProject)
	out := captureStdout(t, func() {
		if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"),
			installOptions{prove: redPushGate}); err != nil {
			t.Fatalf("installHooks: %v", err)
		}
	})
	hooks := filepath.Join(f.repo, ".git", "hooks")
	for _, name := range []string{"pre-commit", "pre-push", "prepare-commit-msg"} {
		if _, err := os.Stat(filepath.Join(hooks, name)); !os.IsNotExist(err) {
			t.Errorf("failed proof left partial hook %s behind (stat err = %v)", name, err)
		}
	}
	if got := localHooksPath(t, f); got != "" {
		t.Errorf("failed proof left core.hooksPath = %q", got)
	}
	if !strings.Contains(out, "remain unchanged") {
		t.Errorf("output = %q, want unchanged prior state named", out)
	}
}

func TestHooksInstall_FailedProofRestoresManagedHooksForwardersAndConfigByteForByte(t *testing.T) {
	f := newChainFixture(t)
	writeRepoFile(t, filepath.Join(f.repo, ".sparkwing", "sparkwing.yaml"), twoGateProject)
	captureStdout(t, func() {
		if _, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"), installOptions{}); err != nil {
			t.Fatalf("initial install: %v", err)
		}
	})

	hooks := filepath.Join(f.repo, ".git", "hooks")
	names := []string{"pre-commit", "pre-push", "prepare-commit-msg"}
	prior := map[string]string{}
	modes := map[string]os.FileMode{}
	for i, name := range names {
		path := filepath.Join(hooks, name)
		body := readRepoFile(t, path) + "# preserved prior " + name + "\n"
		writeExec(t, path, body)
		mode := os.FileMode(0o700 + i*0o10)
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		prior[name] = body
		modes[name] = mode
	}
	priorConfig := f.git(t, "config", "--local", "core.hooksPath")

	out := captureStdout(t, func() {
		gated, err := installHooks(f.tryGit, f.repo, filepath.Join(f.repo, ".sparkwing"), installOptions{prove: redPushGate})
		if err != nil {
			t.Fatalf("failed reinstall: %v", err)
		}
		if !gated {
			t.Error("restored managed gates were reported ungated")
		}
	})
	for _, name := range names {
		path := filepath.Join(hooks, name)
		if got := readRepoFile(t, path); got != prior[name] {
			t.Errorf("%s changed across failed proof", name)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != modes[name] {
			t.Errorf("%s mode = %o, want %o", name, got, modes[name])
		}
	}
	if got := f.git(t, "config", "--local", "core.hooksPath"); got != priorConfig {
		t.Errorf("core.hooksPath changed from %q to %q", priorConfig, got)
	}
	if !strings.Contains(out, "remain unchanged") {
		t.Errorf("output = %q, want unchanged prior state named", out)
	}
}

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
	if ran := f.ranPipelines(t); !strings.Contains(ran, "run old-gate") || strings.Contains(ran, "run gate\n") {
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
	if ran := f.ranPipelines(t); !strings.Contains(ran, "run gate\n") {
		t.Fatalf("published replacement hook did not fire: %q", ran)
	}
}

func TestHooksInstall_GlobalHookChangesDuringProofPublishNothing(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *chainFixture)
	}{
		{
			name: "addition",
			mutate: func(t *testing.T, f *chainFixture) {
				writeExec(t, filepath.Join(f.globalDir, "commit-msg"), "#!/bin/sh\nexit 0\n")
			},
		},
		{
			name: "removal",
			mutate: func(t *testing.T, f *chainFixture) {
				if err := os.Remove(filepath.Join(f.globalDir, "prepare-commit-msg")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "identity",
			mutate: func(t *testing.T, f *chainFixture) {
				path := filepath.Join(f.globalDir, "pre-commit")
				body := readRepoFile(t, path)
				if err := os.Rename(path, path+".prior"); err != nil {
					t.Fatal(err)
				}
				writeExec(t, path, body)
			},
		},
		{
			name: "content",
			mutate: func(t *testing.T, f *chainFixture) {
				if err := os.WriteFile(filepath.Join(f.globalDir, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			tt.mutate(t, f)
			close(releaseProof)

			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "global hooks changed while hook gates were proving") {
					t.Fatalf("install error = %v, want the concurrent global-hook change", err)
				}
			case <-time.After(time.Second):
				t.Fatal("installer did not reject the changed global hooks")
			}
			if got := readRepoFile(t, priorPath); got != priorBody {
				t.Fatalf("rejected install changed the prior hook:\n%s", got)
			}
			if got := f.git(t, "config", "--local", "core.hooksPath"); got != priorConfig {
				t.Fatalf("rejected install changed core.hooksPath from %q to %q", priorConfig, got)
			}
			for _, name := range []string{"pre-push", "prepare-commit-msg"} {
				if _, err := os.Stat(filepath.Join(hooks, name)); !os.IsNotExist(err) {
					t.Errorf("rejected install published %s (stat err = %v)", name, err)
				}
			}
		})
	}
}

func TestHooksInstall_GlobalHooksPathChangesDuringProofPublishNothing(t *testing.T) {
	f := newChainFixture(t)
	hooks := filepath.Join(f.repo, ".git", "hooks")
	priorPath := filepath.Join(hooks, "pre-commit")
	priorBody := renderHookScript("pre-commit", []string{"old-gate"}, false, "")
	writeExec(t, priorPath, priorBody)
	f.git(t, "config", "core.hooksPath", hooks)
	priorConfig := f.git(t, "config", "--local", "core.hooksPath")

	replacement := filepath.Join(f.root, "replacement-globalhooks")
	if err := os.Mkdir(replacement, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pre-commit", "prepare-commit-msg"} {
		if err := os.Link(filepath.Join(f.globalDir, name), filepath.Join(replacement, name)); err != nil {
			t.Fatal(err)
		}
	}

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
	f.git(t, "config", "--global", "core.hooksPath", replacement)
	close(releaseProof)

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "global hooks changed while hook gates were proving") {
			t.Fatalf("install error = %v, want the concurrent global hooks path change", err)
		}
	case <-time.After(time.Second):
		t.Fatal("installer did not reject the changed global hooks path")
	}
	if got := readRepoFile(t, priorPath); got != priorBody {
		t.Fatalf("rejected install changed the prior hook:\n%s", got)
	}
	if got := f.git(t, "config", "--local", "core.hooksPath"); got != priorConfig {
		t.Fatalf("rejected install changed core.hooksPath from %q to %q", priorConfig, got)
	}
	for _, name := range []string{"pre-push", "prepare-commit-msg"} {
		if _, err := os.Stat(filepath.Join(hooks, name)); !os.IsNotExist(err) {
			t.Errorf("rejected install published %s (stat err = %v)", name, err)
		}
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
	if !strings.Contains(out, "remain unchanged") {
		t.Errorf("output = %q, want it to say the prior state remained unchanged", out)
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
	names, err := declaredHookNames(f.repo)
	if err != nil {
		t.Fatalf("declaredHookNames: %v", err)
	}
	got := strings.Join(names, ",")
	if got != "pre-commit,pre-push" {
		t.Errorf("declaredHookNames = %q, want %q", got, "pre-commit,pre-push")
	}
}

func TestDeclaredHookNames_UnreadableProjectDeclaresNothing(t *testing.T) {
	got, err := declaredHookNames(t.TempDir())
	if err != nil {
		t.Fatalf("declaredHookNames: %v", err)
	}
	if got != nil {
		t.Errorf("declaredHookNames = %v, want nil for a directory with no project", got)
	}
}
