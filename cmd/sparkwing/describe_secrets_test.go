package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

const describeSecretsSource = `package main

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/pkg/runner"
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
	runner.Main()
}
`

// safety: describe --secrets is what an operator checks before setting a
// secret, so a stale or empty answer after a declaration changes, or a
// success when the pipeline cannot describe itself, sends them wrong.
func TestPipelineDescribeSecretsFollowsTheSourceAndFailsLoudly(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: builds the CLI and compiles a pipeline twice")
	}
	e := newSubmitTestEnv(t)
	describe := func(name string) {
		t.Helper()
		e.useHostedFixture(t, strings.Replace(describeSecretsSource, "NAME", name, 1))
		stdout, stderr, err := e.runStdout("pipeline", "describe", "--pipeline", "fixture", "--secrets", "-o", "json")
		if err != nil {
			t.Fatalf("describe --secrets: %v\n%s", err, stderr)
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
	describe("FIRST_TOKEN")
	describe("RENAMED_TOKEN")

	writeSDKFixture(t, filepath.Join(e.repoDir, ".sparkwing"), "package main\n\nfunc main() { undefined() }\n")
	if out, err := e.run("pipeline", "describe", "--pipeline", "fixture", "--secrets", "-o", "json"); err == nil {
		t.Fatalf("describe --secrets of a pipeline that cannot build succeeded:\n%s", out)
	}
}
