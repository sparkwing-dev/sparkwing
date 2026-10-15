//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestDebugAttachExecHelper(t *testing.T) {
	if !slices.Contains(os.Args, "--debug-attach-fixture") {
		return
	}
	if err := attachDebugExecution(t.Context(), controllerwire.DebugExecution{Kind: "kubernetes", Runner: "cloud-1", Namespace: "ci", Pod: "job-1", Container: "runner"}, os.Stdout); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestDebugAttachSelectsTheExecutionContainerInRealProcess(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "kubectl")
	script := "#!/bin/sh\nprintf 'arg:%s\\n' \"$@\"\nprintf 'controller:%s\\nkubeconfig:%s\\n' \"${CONTROLLER_TOKEN-unset}\" \"$KUBECONFIG\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), self, "-test.run=^TestDebugAttachExecHelper$", "--", "--debug-attach-fixture")
	cmd.Dir, cmd.Env = dir, []string{"PATH=" + dir, "HOME=" + dir, "KUBECONFIG=" + filepath.Join(dir, "config"), "CONTROLLER_TOKEN=private"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("attachment fixture: %v\n%s", err, out)
	}
	want := "arg:exec\narg:-it\narg:--namespace\narg:ci\narg:--container\narg:runner\narg:job-1\narg:--\narg:bash\ncontroller:unset\nkubeconfig:" + filepath.Join(dir, "config") + "\n"
	if string(out) != want {
		t.Fatalf("attachment invocation changed: %q", out)
	}
}
