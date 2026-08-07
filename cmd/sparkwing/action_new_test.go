package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
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
		{"ci-pr-check", triggerBlocks["pull_request"], func(t *testing.T, tr pipelines.Triggers) {
			if tr.PullRequest == nil {
				t.Error("ci-pr-check declares no pull_request trigger; the shape is named for the event it does not fire on")
			}
			if len(tr.PullRequest.Branches) > 0 {
				t.Errorf("pull_request pins branches %v; shapes must be correct in a repo that uses any branch name", tr.PullRequest.Branches)
			}
		}},
		{"scheduled-report", triggerBlocks["schedule"], func(t *testing.T, tr pipelines.Triggers) {
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

// TestEveryTriggerBlockParses holds the --on vocabulary to what the
// strict config parser accepts. Hand-written yaml fails silently here:
// `push:` with no value is legal and decodes to nothing, so a typo
// produces a pipeline that lints, explains, and never fires.
func TestEveryTriggerBlockParses(t *testing.T) {
	for _, event := range triggerEventNames {
		t.Run(event, func(t *testing.T) {
			block, ok := triggerBlocks[event]
			if !ok {
				t.Fatalf("--on offers %q with no block to write", event)
			}
			dir := t.TempDir()
			path := filepath.Join(dir, projectconfig.Filename)
			if err := os.WriteFile(path, []byte("pipelines:\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := appendPipelinesYAML(dir, "sample", "Sample", false, block); err != nil {
				t.Fatal(err)
			}
			cfg, err := projectconfig.Load(path)
			if err != nil {
				t.Fatalf("--on %s emits yaml the parser rejects: %v", event, err)
			}
			on := cfg.Pipelines[0].On
			if event == "manual" {
				if (on != pipelines.Triggers{}) {
					t.Errorf("--on manual declared %+v; it is the opt-out", on)
				}
				return
			}
			if (on == pipelines.Triggers{}) {
				t.Errorf("--on %s decoded to no trigger at all", event)
			}
		})
	}
}

// Shape and trigger are independent choices. The combination three
// agent trials asked for -- one node, fired by pull requests -- has to
// be one command, not a three-node gate with two nodes deleted.
func TestOnOverridesTheShapeDefault(t *testing.T) {
	cases := []struct {
		shape    string
		on       string
		explicit bool
		want     string
	}{
		{"minimal", "pull_request", true, "pull_request"},
		{"ci-pr-check", "manual", true, ""},
		{"ci-pr-check", "", false, "pull_request"},
		{"scheduled-report", "", false, "schedule"},
		{"release", "", false, ""},
		{"release", "push", true, "push"},
	}
	for _, tc := range cases {
		shape, ok := builtinShapeByName(tc.shape)
		if !ok {
			t.Fatalf("no shape %q", tc.shape)
		}
		got, err := resolveTrigger(shape, tc.on, tc.explicit)
		if err != nil {
			t.Fatalf("%s --on %q: %v", tc.shape, tc.on, err)
		}
		if got != triggerBlocks[tc.want] && !(tc.want == "" && got == "") {
			t.Errorf("%s --on %q (explicit=%v) wrote %q; want the %q block",
				tc.shape, tc.on, tc.explicit, got, tc.want)
		}
	}
}

// An unknown --on must name the whole vocabulary. A rejection that does
// not is a round-trip: the author still has to go find the list.
func TestUnknownTriggerNamesEveryChoice(t *testing.T) {
	shape, _ := builtinShapeByName("minimal")
	_, err := resolveTrigger(shape, "on_merge", true)
	if err == nil {
		t.Fatal("accepted an unknown trigger")
	}
	for _, event := range triggerEventNames {
		if !strings.Contains(err.Error(), event) {
			t.Errorf("rejection does not offer %q: %v", event, err)
		}
	}
}

// Generated code must not name SDK functions that do not exist. The
// minimal stub pointed at `ExecIn` / `BashIn`, which the SDK has never
// had -- it exports Exec and Bash -- and that comment is the first
// thing an editor reads. Every agent in a six-config sweep searched the
// docs for those spellings, found nothing, and fell back to reading the
// whole SDK reference.
func TestScaffoldsNameOnlyRealSDKSymbols(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate the SDK source")
	}
	sdk := filepath.Join(filepath.Dir(filename), "..", "..", "sparkwing")
	entries, err := os.ReadDir(sdk)
	if err != nil {
		t.Fatal(err)
	}
	var src strings.Builder
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(sdk, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		src.Write(b)
	}
	sdkSrc := src.String()

	// Comments count. The bug this guards was in prose -- "Paths in
	// ExecIn / BashIn / ReadFile" -- not in a call, so checking only
	// sw.-qualified calls would have passed while the defect shipped.
	// Multi-segment CamelCase is the shape of an SDK symbol and not of
	// English, so every such token in the rendered template has to
	// resolve, whether it is called or merely mentioned.
	ref := regexp.MustCompile(`\b(?:sw\.)?([A-Z][a-z0-9]+(?:[A-Z][a-z0-9]*)+)\b`)
	// The scaffolder's own generated identifiers, and the one stdlib
	// name the templates use.
	generated := map[string]bool{"Sample": true, "SampleJob": true, "Context": true}
	for _, shape := range builtinShapes {
		rendered := renderBuiltinTemplate("sample", "", shape.src)
		for _, m := range ref.FindAllStringSubmatch(rendered, -1) {
			symbol := m[1]
			if generated[symbol] || strings.HasPrefix(symbol, "Sample") {
				continue
			}
			if !strings.Contains(sdkSrc, symbol) {
				t.Errorf("%s names %s, which does not exist in the SDK", shape.Name, symbol)
			}
		}
	}
}
