package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func parseHooksUninstall(args []string) error {
	return runSparkwing(append([]string{"pipeline", "hooks", "uninstall"}, args...))
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
