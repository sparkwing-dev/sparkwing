package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func TestRegisteredFlagsReachExecutionParser(t *testing.T) {
	for _, command := range clisurface.Registry() {
		if !strings.HasPrefix(command.Path, "sparkwing ") || len(childCommands(command.Path)) != 0 {
			continue
		}
		for _, f := range append(clisurface.GlobalFlags(), command.Flags...) {
			t.Run(command.Path+"/"+f.Name, func(t *testing.T) {
				args := strings.Fields(strings.TrimPrefix(command.Path, "sparkwing "))
				if command.PipelineArgument && command.Path != "sparkwing run" {
					args = append(args, "example")
				}
				value := "example"
				switch {
				case len(f.Values) > 0:
					value = f.Values[0]
				case f.Argument == "START_LINE" || f.Argument == "N" || f.Argument == "BYTES" || f.HasMinimum:
					value = "1"
				case f.Argument == "DURATION" || f.Argument == "DUR":
					value = "1s"
				case f.Name == "output" || f.Name == "sw-output":
					value = "json"
				}
				token := "--" + f.Name
				if f.Argument != "" {
					token += "=" + value
				}
				tokenIndex := len(args)
				args = append(args, token)
				if f.Name != "help" {
					args = append(args, "--help")
				}
				var err error
				captureStdout(t, func() { err = runSparkwing(args) })
				if err != nil {
					t.Fatal(err)
				}
				if f.Short != "" {
					token = "-" + f.Short
					if f.Argument != "" {
						token += "=" + value
					}
					args[tokenIndex] = token
					captureStdout(t, func() { err = runSparkwing(args) })
					if err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func TestHooksCommandsUseRegistryGrammarAndJSON(t *testing.T) {
	f := newChainFixture(t)
	f.asProcessEnv(t)
	t.Chdir(f.repo)
	for _, args := range [][]string{
		{"pipeline", "hooks", "install"},
		{"pipeline", "hooks", "status"},
		{"pipeline", "hooks", "uninstall", "-o", "json"},
	} {
		var err error
		output := captureStdout(t, func() { err = runSparkwing(args) })
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if strings.TrimSpace(output) == "" {
			t.Fatalf("%v: missing JSON receipt", args)
		}
		for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
			if !json.Valid([]byte(line)) {
				t.Fatalf("%v emitted prose on stdout: %s", args, output)
			}
		}
	}
	for _, flag := range []string{"--fleet", "--all", "--all-repos", "--ungated"} {
		if err := runSparkwing([]string{"pipeline", "hooks", "status", flag}); exitCodeFor(err) != 2 {
			t.Fatalf("%s: %v", flag, err)
		}
	}
	for _, flag := range []string{"--all-repos", "--no-prove"} {
		if err := runSparkwing([]string{"pipeline", "hooks", "install", flag}); exitCodeFor(err) != 2 {
			t.Fatalf("%s: %v", flag, err)
		}
	}

}

func parseHooksInstall(args []string) error {
	return runSparkwing(append([]string{"pipeline", "hooks", "install"}, args...))
}

func parseHooksUninstall(args []string) error {
	return runSparkwing(append([]string{"pipeline", "hooks", "uninstall"}, args...))
}

func parseHooksStatus(args []string) error {
	return runSparkwing(append([]string{"pipeline", "hooks", "status"}, args...))
}

func TestPipelineNewUsesJSONReceipt(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, ".sparkwing")
	if err := os.MkdirAll(filepath.Join(dir, "jobs"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeSDKFixture(t, dir, `package main
import sw "github.com/sparkwing-dev/sparkwing/sparkwing"
func main(){sw.Main()}
`)
	writeFile(t, filepath.Join(dir, "sparkwing.yaml"), "pipelines: []\n")
	t.Setenv("GOPROXY", "off")
	t.Chdir(repo)
	for _, format := range []string{"", "json", "plain"} {
		name := "sample-" + format
		if format == "" {
			name = "sample-default"
		}
		args := []string{"pipeline", "new", "--pipeline=" + name, "--template=minimal"}
		if format != "" {
			args = append(args, "-o", format)
		}
		var err error
		out := captureStdout(t, func() {
			err = runSparkwing(args)
		})
		if err != nil {
			t.Fatal(err)
		}
		if format != "plain" {
			var receipt struct {
				Pipeline string `json:"pipeline"`
				Path     string `json:"path"`
			}
			if err := json.Unmarshal([]byte(out), &receipt); err != nil || receipt.Pipeline != name || receipt.Path == "" {
				t.Fatalf("invalid scaffold receipt: %s (%v)", out, err)
			}
		} else if strings.TrimSpace(out) != "sample-plain" {
			t.Fatalf("plain scaffold receipt: %s", out)
		}
	}
}

func parsePipelineNew(args []string) error {
	return runSparkwing(append([]string{"pipeline", "new"}, args...))
}
