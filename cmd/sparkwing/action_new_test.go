package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/pipelinelint"
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
