package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpawnGuardDiscoversTypedBypasses(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module " + modulePath + "\n\ngo 1.26.0\n",
		"newpackage/spawn.go": `package newpackage
import (renamed "os/exec"; "os"; "syscall")
var constructor = renamed.CommandContext
var start = os.StartProcess
var replace = syscall.Exec
var raw renamed.Cmd
var allocated = new(renamed.Cmd)
var literal = renamed.Cmd{}
type embedded struct { renamed.Cmd }
var commands []renamed.Cmd
var lookup map[string]renamed.Cmd
var channel chan renamed.Cmd
type derived renamed.Cmd
func override(c *renamed.Cmd) { target := &c.Env; *target = nil }
func zero[T any]() *T { return new(T) }
var genericAllocation = zero[renamed.Cmd]()
`,
		"windowsonly/spawn_windows.go": `package windowsonly
import "os/exec"
var factory = exec.Command
`,
		"armonly/spawn_arm64.go": `package armonly
import "os/exec"
var factory = exec.CommandContext
`,
		"dot/spawn.go": `package dot
import . "os/exec"
var factory = Command
`,
		"internal/procenv/spawn.go": `package procenv
import "os/exec"
func Command(env []string) *exec.Cmd { c:=exec.Command("echo"); c.Env=env; return c }
`,
		"safe/spawn.go": `package safe
import("os/exec"; "github.com/sparkwing-dev/sparkwing/internal/procenv")
func command() *exec.Cmd { c:=procenv.Command([]string{}); c.Dir="/tmp"; return c }
func carry[T any](value T) T { return value }
var carried = carry[*exec.Cmd](command())
`,
	}
	for name, source := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	findings, err := spawnFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 16 {
		t.Fatalf("want 16 bypasses, got %d:\n%s", len(findings), strings.Join(findings, "\n"))
	}
	for _, want := range []string{"os/exec.CommandContext", "os.StartProcess", "syscall.Exec", "value construction", "generic value construction", "allocation", "literal", "direct exec.Cmd.Env access", "os/exec.Command", "windowsonly", "armonly"} {
		if !strings.Contains(strings.Join(findings, "\n"), want) {
			t.Errorf("missing %q in %v", want, findings)
		}
	}
}

func TestModuleSpawnsUseEnvironmentOwner(t *testing.T) {
	if testing.Short() {
		t.Skip("whole-module type loading")
	}
	findings, err := spawnFindings(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) > 0 {
		t.Fatal(strings.Join(findings, "\n"))
	}
}
