//go:build windows

package procenv

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
		{"empty PATH", []string{"PATH="}, bin, "tool", filepath.Join(bin, "tool.com")},
		{"current directory", []string{"PATH=missing", "PATHEXT=.exe"}, bin, "tool", filepath.Join(bin, "tool.exe")},
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
