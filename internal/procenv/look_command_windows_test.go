//go:build windows

package procenv

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

func TestSDKCommandWindowsRefusesBatchArguments(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "Program Files (x86)")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(bin, "tool.cmd")
	if err := os.WriteFile(tool, []byte("@echo off\r\necho tool\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, contextBound := range []bool{false, true} {
		for _, arg := range []string{"plain", "main&ver&rem", "%FIXTURE%"} {
			cmd := SDKCommand(nil, dir, tool, arg)
			if contextBound {
				cmd = SDKCommandContext(t.Context(), nil, dir, tool, arg)
			}
			if cmd.Path != tool || cmd.Args[0] != tool || !filepath.IsAbs(cmd.Path) {
				t.Fatalf("command not fully resolved: %q, %q", cmd.Path, cmd.Args)
			}
			if arg == "plain" {
				if cmd.Err != nil {
					t.Fatalf("context=%v: safe batch argument refused: %v", contextBound, cmd.Err)
				}
			} else if cmd.Err == nil || !strings.Contains(cmd.Err.Error(), "argv[1]") || !strings.Contains(cmd.Err.Error(), "non-batch executable") || strings.Contains(cmd.Err.Error(), arg) {
				t.Fatalf("context=%v: unsafe batch argument error: %v", contextBound, cmd.Err)
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
