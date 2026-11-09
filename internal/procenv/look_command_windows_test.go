//go:build windows

package procenv

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestLookupCommandWindowsEnvironment(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin with spaces")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"tool.exe", "tool.com", "tool.custom", "tool", "direct.exe"} {
		if err := os.WriteFile(filepath.Join(bin, file), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	for _, tc := range []struct {
		name string
		env  []string
		cwd  string
		argv string
		want string
	}{
		{"case folded PATH and PATHEXT", []string{"Path=" + bin, "PathExt=.EXE;.COM"}, dir, "tool", filepath.Join(bin, "tool.EXE")},
		{"PATHEXT order", []string{"PATH=" + bin, "PATHEXT=.com;.exe"}, dir, "tool", filepath.Join(bin, "tool.com")},
		{"custom extension", []string{"PATH=" + bin, "PATHEXT=custom"}, dir, "tool", filepath.Join(bin, "tool.custom")},
		{"later values win", []string{"PATH=missing", "Path=" + bin, "PATHEXT=.absent", "PathExt=.exe"}, dir, "tool", filepath.Join(bin, "tool.exe")},
		{"absent PATH", nil, dir, "tool", filepath.Join(bin, "tool.com")},
		{"quoted PATH", []string{`PATH="` + bin + `"`, "PATHEXT=.exe"}, dir, "tool", filepath.Join(bin, "tool.exe")},
		{"relative PATH", []string{"PATH=bin with spaces", "PATHEXT=.exe"}, dir, "tool", filepath.Join(bin, "tool.exe")},
		{"explicit current directory in PATH", []string{"PATH=.", "PATHEXT=.exe"}, bin, "tool", filepath.Join(bin, "tool.exe")},
		{"explicit relative path", []string{"PATH=missing", "PATHEXT=.exe"}, dir, `bin with spaces\direct`, filepath.Join(bin, "direct.exe")},
		{"root relative path", []string{"PATH=missing", "PATHEXT=.exe"}, dir, filepath.Join(bin, "direct")[len(filepath.VolumeName(bin)):], filepath.Join(bin, "direct.exe")},
		{"drive relative path", []string{"PATH=missing", "PATHEXT=.exe"}, bin, filepath.VolumeName(bin) + "direct", filepath.Join(bin, "direct.exe")},
		{"absolute path", []string{"PATH=missing", "PATHEXT=.exe"}, dir, filepath.Join(bin, "direct"), filepath.Join(bin, "direct.exe")},
		{"explicit extension", []string{"PATH=" + bin, "PATHEXT=.com"}, dir, "tool.exe", filepath.Join(bin, "tool.exe")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LookupCommand(tc.argv, tc.env, tc.cwd)
			if err != nil || got != tc.want {
				t.Fatalf("LookupCommand=%q, %v; want %q", got, err, tc.want)
			}
		})
	}
	for _, argv := range []string{"missing", `missing\tool`, "bin with spaces"} {
		if _, err := LookupCommand(argv, []string{"PATH=" + bin}, dir); !errors.Is(err, exec.ErrNotFound) {
			t.Fatalf("missing command %q: %v", argv, err)
		}
	}
}

func TestSDKCommandWindowsBatchArguments(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "Program Files (x86) & Tools")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(bin, "tool.cmd")
	if err := os.WriteFile(tool, []byte("@echo off\r\necho tool\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	for _, contextBound := range []bool{false, true} {
		for _, arg := range []string{"plain", "main&ver&rem", "%FIXTURE%", "x\ny"} {
			cmd := SDKCommand(nil, dir, tool, arg)
			if contextBound {
				cmd = SDKCommandContext(t.Context(), nil, dir, tool, arg)
			}
			if len(cmd.Args) != 2 || cmd.Args[0] != tool || cmd.Args[1] != arg {
				t.Fatalf("argv changed: %q", cmd.Args)
			}
			if arg == "x\ny" {
				if cmd.Err == nil || !strings.Contains(cmd.Err.Error(), "argv[1]") || !strings.Contains(cmd.Err.Error(), "non-batch executable") {
					t.Fatalf("context=%v: unescapable batch argument: %v", contextBound, cmd.Err)
				}
				continue
			}
			want, err := batchCommandLine(tool, []string{arg})
			if err != nil || cmd.Err != nil || !strings.EqualFold(cmd.Path, filepath.Join(system, "cmd.exe")) || cmd.SysProcAttr == nil || cmd.SysProcAttr.CmdLine != want {
				t.Fatalf("context=%v: batch command for %q: path=%q, err=%v, attr=%+v", contextBound, arg, cmd.Path, cmd.Err, cmd.SysProcAttr)
			}
			output, err := cmd.Output()
			if err != nil || strings.TrimSpace(string(output)) != "tool" {
				t.Fatalf("context=%v: batch run for %q: %v, output=%q", contextBound, arg, err, output)
			}
		}
	}
}

func TestSDKCommandWindowsPreservesBashArgv(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "bash.exe")
	if err := os.WriteFile(tool, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, program := range []string{"", "printf value", strings.Repeat(":\n", 17000)} {
		for _, contextBound := range []bool{false, true} {
			cmd := SDKCommand([]string{"SPARKWING_BASH_PROGRAM=caller-value"}, dir, tool, "-c", program)
			if contextBound {
				cmd = SDKCommandContext(t.Context(), []string{"SPARKWING_BASH_PROGRAM=caller-value"}, dir, tool, "-c", program)
			}
			if cmd.Err != nil || len(cmd.Args) != 3 || cmd.Args[2] != program || len(cmd.Env) != 1 || cmd.Env[0] != "SPARKWING_BASH_PROGRAM=caller-value" {
				t.Fatalf("shared constructor changed Bash argv or environment: context=%v, err=%v", contextBound, cmd.Err)
			}
		}
	}
}

//sparkwing:critical
func TestLookupCommandWindowsIgnoresImplicitWorkingDirectory(t *testing.T) {
	dir, bin := t.TempDir(), t.TempDir()
	for _, file := range []string{filepath.Join(dir, "go.bat"), filepath.Join(bin, "go.exe")} {
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{bin, ";" + bin + ";;"} {
		got, err := LookupCommand("go", []string{"PATH=" + path, "PATHEXT=.bat;.exe"}, dir)
		if err != nil || got != filepath.Join(bin, "go.exe") {
			t.Fatalf("PATH=%q: got %q, %v", path, got, err)
		}
	}
	for _, path := range []string{"", "missing", ";missing;;"} {
		if _, err := LookupCommand("go", []string{"PATH=" + path}, dir); !errors.Is(err, exec.ErrNotFound) {
			t.Fatalf("PATH=%q: planted batch file resolved: %v", path, err)
		}
	}
}
