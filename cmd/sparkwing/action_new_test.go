package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/pipelinelint"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// TestBuiltinTemplatesRenderLintClean renders every built-in scaffold
// template and asserts it is valid Go and passes the pipeline linter
// with zero findings -- the machine-checkable half of "idiomatic by
// construction" promised by `sparkwing pipeline new`.
func TestBuiltinTemplatesRenderLintClean(t *testing.T) {
	cases := []struct {
		template string
		src      string
	}{
		{"minimal", minimalTemplate},
		{"build-test-deploy", buildTestDeployTemplate},
		{"ci-pr-check", ciPRCheckTemplate},
		{"release", releaseTemplate},
		{"scheduled-report", scheduledReportTemplate},
	}
	for _, tc := range cases {
		t.Run(tc.template, func(t *testing.T) {
			rendered := renderBuiltinTemplate("sample-report", "", tc.src)

			if _, err := parser.ParseFile(token.NewFileSet(), "job.go", rendered, parser.AllErrors); err != nil {
				t.Fatalf("rendered template does not parse as Go: %v", err)
			}

			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "job.go"), []byte(rendered), 0o644); err != nil {
				t.Fatal(err)
			}
			findings, err := pipelinelint.AnalyzeSource(dir)
			if err != nil {
				t.Fatalf("AnalyzeSource: %v", err)
			}
			if len(findings) > 0 {
				t.Fatalf("template %q is not lint-clean: %+v", tc.template, findings)
			}
		})
	}
}

// TestShapesNamedForAnEventCarryItsTrigger asserts the two shapes named
// for an event write that event's `on:` block, and that the block
// survives the strict config parser.
//
// A hand-written yaml fragment is the kind of thing that decodes as
// nothing and reports nothing: `pull_request:` with no value is legal
// yaml, so a typo here would produce a pipeline that lints, explains,
// and never fires. Six agent trials each hit the gap this closes; an
// unparsed trigger would leave the gap while looking closed.
func TestShapesNamedForAnEventCarryItsTrigger(t *testing.T) {
	cases := []struct {
		shape   string
		trigger string
		check   func(*testing.T, pipelines.Triggers)
	}{
		{"ci-pr-check", prCheckTrigger, func(t *testing.T, tr pipelines.Triggers) {
			if tr.PullRequest == nil {
				t.Error("ci-pr-check declares no pull_request trigger; the shape is named for the event it does not fire on")
			}
			if len(tr.PullRequest.Branches) > 0 {
				t.Errorf("pull_request pins branches %v; shapes must be correct in a repo that uses any branch name", tr.PullRequest.Branches)
			}
		}},
		{"scheduled-report", scheduledReportTrigger, func(t *testing.T, tr pipelines.Triggers) {
			if tr.Schedule == "" {
				t.Error("scheduled-report declares no schedule; its help says it prints one")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.shape, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, projectconfig.Filename)
			if err := os.WriteFile(path, []byte("pipelines:\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := appendPipelinesYAML(dir, "sample", "Sample", false, tc.trigger); err != nil {
				t.Fatal(err)
			}
			cfg, err := projectconfig.Load(path)
			if err != nil {
				t.Fatalf("scaffolded config does not parse: %v", err)
			}
			if len(cfg.Pipelines) != 1 {
				t.Fatalf("got %d pipelines, want 1", len(cfg.Pipelines))
			}
			if (cfg.Pipelines[0].On == pipelines.Triggers{}) {
				t.Fatal("entry has no triggers at all")
			}
			tc.check(t, cfg.Pipelines[0].On)
		})
	}
}

// TestShapesWithoutAnEventStayManual guards the other direction: a shape
// that is purely structural must not invent a trigger, or `pipeline new`
// starts wiring repos into events nobody asked for.
func TestShapesWithoutAnEventStayManual(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, projectconfig.Filename)
	if err := os.WriteFile(path, []byte("pipelines:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := appendPipelinesYAML(dir, "sample", "Sample", false, ""); err != nil {
		t.Fatal(err)
	}
	cfg, err := projectconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if (cfg.Pipelines[0].On != pipelines.Triggers{}) {
		t.Errorf("structural shape declared triggers %+v", cfg.Pipelines[0].On)
	}
}

func TestGoJobFilename(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain kebab", "release", "release.go"},
		{"multi-word kebab", "build-test-deploy", "build_test_deploy.go"},
		{"trailing -test would shadow as Go test file", "backend-test", "backend_test_pipeline.go"},
		{"single-word _test", "test", "test.go"},
		{"already-snake _test suffix", "smoke-test", "smoke_test_pipeline.go"},
		{"trailing -linux would build-tag the file", "frontend-linux", "frontend_linux_pipeline.go"},
		{"trailing -darwin", "agent-darwin", "agent_darwin_pipeline.go"},
		{"trailing -windows", "service-windows", "service_windows_pipeline.go"},
		{"single-word linux is fine", "linux", "linux.go"},
		{"trailing -arm64", "worker-arm64", "worker_arm64_pipeline.go"},
		{"trailing -amd64", "service-amd64", "service_amd64_pipeline.go"},
		{"trailing -linux-amd64 (last token reserved)", "service-linux-amd64", "service_linux_amd64_pipeline.go"},
		{"trailing -windows-arm64", "agent-windows-arm64", "agent_windows_arm64_pipeline.go"},
		{"trailing -linux-test", "smoke-linux-test", "smoke_linux_test_pipeline.go"},
		{"reserved token mid-name is fine", "deploy-linux-server", "deploy_linux_server.go"},
		{"underscore prefix would be excluded by go build", "_internal", "pipeline__internal.go"},
		{"dot prefix would be excluded by go build", ".hidden", "pipeline_.hidden.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := goJobFilename(tc.in)
			if got != tc.want {
				t.Fatalf("goJobFilename(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}
