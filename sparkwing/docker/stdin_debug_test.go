package docker

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestShellStdinPlumbing is a canary for TestLoginSecretNotInArgv: it
// asserts that a `#!/bin/sh` fake docker can receive stdin from
// os/exec and redirect it to a file. When this test fails, the
// Login-via-stdin test will fail for the same root cause (PATH
// restriction, missing /bin/cat, etc.) and pinpoint the plumbing
// rather than the production code.
func TestShellStdinPlumbing(t *testing.T) {
	dir := t.TempDir()
	stdinFile := filepath.Join(dir, "stdin")

	binDir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\n/bin/cat > %q\nexit 0\n", stdinFile)
	fake := filepath.Join(binDir, "mycmd")
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	cmd := exec.Command(fake, "arg1")
	cmd.Stdin = strings.NewReader("hello-stdin")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v stderr=%s", err, errb.String())
	}

	b, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b) != "hello-stdin" {
		t.Fatalf("stdin = %q, want %q", string(b), "hello-stdin")
	}
}
