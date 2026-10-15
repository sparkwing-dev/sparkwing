package pipelineentry

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
	lookPath := func(name string) (string, error) {
		if name == "npm" {
			return npm, nil
		}
		if name == "node" {
			return node, nil
		}
		return "", exec.ErrNotFound
	}
	args := []string{"exec", "--offline", "--no", "--", "tsc", "--noEmit", "space value", "a&b", "%PATH%", `quote"value`}
	command, err := NPMCommand(t.Context(), "windows", lookPath, args...)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string{node, filepath.Join(directory, "node_modules", "npm", "bin", "npm-cli.js")}, args...)
	if command.Path != node || !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("command = %q %#v, want %#v", command.Path, command.Args, want)
	}
}

func TestNPMCommandPropagatesMissingNpm(t *testing.T) {
	command, err := NPMCommand(t.Context(), "windows", func(name string) (string, error) {
		if name != "npm" {
			t.Fatalf("unexpected %s lookup", name)
		}
		return "", exec.ErrNotFound
	}, "ci")
	if command != nil || !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing npm = %v %v", command, err)
	}
}

func TestNPMCommandPreservesUnixNpmInvocation(t *testing.T) {
	npm := filepath.Join("npm directory with spaces", "npm")
	for _, goos := range []string{"linux", "darwin"} {
		command, err := NPMCommand(t.Context(), goos, func(name string) (string, error) {
			if name != "npm" {
				t.Fatalf("unexpected %s lookup", name)
			}
			return npm, nil
		}, "config", "get", "cache")
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{npm, "config", "get", "cache"}; command.Path != npm || !reflect.DeepEqual(command.Args, want) {
			t.Fatalf("%s command = %#v, want %#v", goos, command.Args, want)
		}
	}
}

func TestNPMCommandPropagatesMissingNode(t *testing.T) {
	command, err := NPMCommand(t.Context(), "windows", func(name string) (string, error) {
		if name == "npm" {
			return "npm.cmd", nil
		}
		return "", exec.ErrNotFound
	}, "config", "get", "cache")
	if command != nil || !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing node = %v %v", command, err)
	}
}

func TestNPMCommandAllowsNativeWindowsNpm(t *testing.T) {
	command, err := NPMCommand(t.Context(), "windows", func(name string) (string, error) {
		if name != "npm" {
			t.Fatalf("unexpected %s lookup", name)
		}
		return "npm.exe", nil
	}, "config", "get", "cache")
	if err != nil || command.Path != "npm.exe" || !reflect.DeepEqual(command.Args, []string{"npm.exe", "config", "get", "cache"}) {
		t.Fatalf("native npm = %v %v", command, err)
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
			command, err := NPMCommand(t.Context(), goos, func(name string) (string, error) {
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
