package main

import "testing"

func TestGoJobFilename(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain kebab", "release", "release.go"},
		{"multi-word kebab", "build-test-deploy", "build_test_deploy.go"},
		// _test.go suffix
		{"trailing -test would shadow as Go test file", "backend-test", "backend_test_pipeline.go"},
		{"single-word _test", "test", "test.go"},
		{"already-snake _test suffix", "smoke-test", "smoke_test_pipeline.go"},
		// _<goos>.go suffix
		{"trailing -linux would build-tag the file", "frontend-linux", "frontend_linux_pipeline.go"},
		{"trailing -darwin", "agent-darwin", "agent_darwin_pipeline.go"},
		{"trailing -windows", "service-windows", "service_windows_pipeline.go"},
		{"single-word linux is fine", "linux", "linux.go"},
		// _<goarch>.go suffix
		{"trailing -arm64", "worker-arm64", "worker_arm64_pipeline.go"},
		{"trailing -amd64", "service-amd64", "service_amd64_pipeline.go"},
		// _<goos>_<goarch>.go compound
		{"trailing -linux-amd64 (last token reserved)", "service-linux-amd64", "service_linux_amd64_pipeline.go"},
		{"trailing -windows-arm64", "agent-windows-arm64", "agent_windows_arm64_pipeline.go"},
		// _<goos>_test.go compound (last token test still triggers)
		{"trailing -linux-test", "smoke-linux-test", "smoke_linux_test_pipeline.go"},
		// reserved token in the middle is harmless (Go only checks trailing)
		{"reserved token mid-name is fine", "deploy-linux-server", "deploy_linux_server.go"},
		// underscore / dot prefix
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
