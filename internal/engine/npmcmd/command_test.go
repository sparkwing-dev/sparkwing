package npmcmd

import (
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNPMCommandUsesWindowsNodeCLI(t *testing.T) {
	directory := filepath.Join("tool directory with spaces", "node")
	npm := filepath.Join(directory, "npm.cmd")
	node := filepath.Join(directory, "node.exe")
	command, err := npmCommand("windows", func(name string) (string, error) {
		switch name {
		case "npm":
			return npm, nil
		case "node":
			return node, nil
		}
		return "", exec.ErrNotFound
	}, "ci", "--no-audit", "--no-fund")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{node, filepath.Join(directory, "node_modules", "npm", "bin", "npm-cli.js"), "ci", "--no-audit", "--no-fund"}
	if command.Path != node || !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("command = %q %#v, want %#v", command.Path, command.Args, want)
	}
}

func TestNPMCommandPreservesNativeInvocation(t *testing.T) {
	for _, tc := range []struct{ goos, name string }{{"linux", "npm"}, {"darwin", "npm"}, {"windows", "npm.EXE"}} {
		t.Run(tc.goos+tc.name, func(t *testing.T) {
			npm := filepath.Join("npm directory with spaces", tc.name)
			command, err := npmCommand(tc.goos, func(name string) (string, error) {
				if name != "npm" {
					t.Fatalf("unexpected %s lookup", name)
				}
				return npm, nil
			}, "config", "get", "cache")
			if err != nil {
				t.Fatal(err)
			}
			if want := []string{npm, "config", "get", "cache"}; command.Path != npm || !reflect.DeepEqual(command.Args, want) {
				t.Fatalf("command = %#v, want %#v", command.Args, want)
			}
		})
	}
}

func TestNPMCommandPropagatesMissingTools(t *testing.T) {
	for _, missing := range []string{"npm", "node"} {
		t.Run(missing, func(t *testing.T) {
			command, err := npmCommand("windows", func(name string) (string, error) {
				if name == missing {
					return "", exec.ErrNotFound
				}
				return "npm.cmd", nil
			}, "install")
			if command != nil || !errors.Is(err, exec.ErrNotFound) {
				t.Fatalf("missing %s = %v %v", missing, command, err)
			}
		})
	}
}

func FuzzNPMCommandArguments(f *testing.F) {
	f.Add("install", "--prefix=directory with spaces")
	f.Add("ci", "& echo credential > marker")
	f.Add("config", "$(touch marker); %PATH% \" ' \\ \n")
	f.Fuzz(func(t *testing.T, first, second string) {
		for _, goos := range []string{"linux", "darwin", "windows"} {
			npm := filepath.Join("tool directory", "npm.cmd")
			node := filepath.Join("tool directory", "node.exe")
			arguments := []string{first, second}
			command, err := npmCommand(goos, func(name string) (string, error) {
				if name == "node" {
					return node, nil
				}
				return npm, nil
			}, arguments...)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{npm, first, second}
			if goos == "windows" {
				want = []string{node, filepath.Join(filepath.Dir(npm), "node_modules", "npm", "bin", "npm-cli.js"), first, second}
			}
			arguments[0] = "changed"
			if !reflect.DeepEqual(command.Args, want) || command.Path != want[0] {
				t.Fatalf("npm arguments changed: %q, want %q", command.Args, want)
			}
		}
	})
}
