package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
)

const describeSecretsSource = `package main

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type declared struct {
	Token string ` + "`sw:\"NAME,required\"`" + `
}

type Fixture struct{ sparkwing.Base }

func (*Fixture) Secrets() any { return &declared{} }

func (*Fixture) Plan(_ context.Context, p *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(p, "work", func(context.Context) error { return nil })
	return nil
}

func main() {
	sparkwing.Register("fixture", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Fixture{} })
	sparkwing.Main()
}
`

func TestPipelineDescribeSecretsFollowsTheSourceAndFailsLoudly(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a Go SDK pipeline twice")
	}
	checkout := t.TempDir()
	dir := filepath.Join(checkout, ".sparkwing")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, filepath.Join(dir, "sparkwing.yaml"), "pipelines:\n  - name: fixture\n    entrypoint: fixture\n")
	binary := filepath.Join(t.TempDir(), "pipeline")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	inspect := func(_ string, inspect func(context.Context, pipelineentry.Command, string) error) error {
		build := exec.CommandContext(t.Context(), "go", "-C", dir, "build", "-mod=mod", "-o", binary, ".")
		build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-buildvcs=false")
		if out, err := build.CombinedOutput(); err != nil {
			return fmt.Errorf("build source: %w: %s", err, out)
		}
		return inspect(t.Context(), pipelineentry.Binary(binary), checkout)
	}
	args := []string{"--pipeline", "fixture", "--secrets", "-o", "json"}
	for _, name := range []string{"FIRST_TOKEN", "RENAMED_TOKEN"} {
		writeSDKFixture(t, dir, strings.Replace(describeSecretsSource, "NAME", name, 1))
		var commandErr error
		stdout := captureStdout(t, func() { commandErr = runPipelineDescribeCommandArgs(args, inspect) })
		if commandErr != nil {
			t.Fatal(commandErr)
		}
		var got struct {
			Secrets []struct {
				Name string `json:"name"`
			} `json:"secrets"`
		}
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("decode %q: %v", stdout, err)
		}
		if len(got.Secrets) != 1 || got.Secrets[0].Name != name {
			t.Fatalf("describe --secrets = %s, want only %s", stdout, name)
		}
	}
	writeSDKFixture(t, dir, "package main\n\nfunc main() { undefined() }\n")
	var commandErr error
	stdout := captureStdout(t, func() { commandErr = runPipelineDescribeCommandArgs(args, inspect) })
	if commandErr == nil || stdout != "" {
		t.Fatalf("unbuildable source returned a successful schema: %v, output=%s", commandErr, stdout)
	}
}
