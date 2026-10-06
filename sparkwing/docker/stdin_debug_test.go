package docker

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testshell"
)

func TestShellStdinPlumbing(t *testing.T) {
	dir := t.TempDir()
	stdinFile := filepath.Join(dir, "stdin")

	binDir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\n/bin/cat > %q\nexit 0\n", filepath.ToSlash(stdinFile))
	fake := filepath.Join(binDir, "mycmd")
	fake = testshell.Install(t, fake, script)

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
