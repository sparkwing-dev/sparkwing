package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSDKDependencyClosure(t *testing.T) {
	for _, tt := range []struct {
		name, sdkImport, leafImport, publicImport string
		wantServer, wantError                     bool
	}{
		{name: "direct", sdkImport: "internal/store", wantServer: true},
		{name: "two hops through pkg", sdkImport: "pkg/cachecontrol", publicImport: "internal/store", wantServer: true},
		{name: "leaf through pkg", sdkImport: "internal/sdkserve", leafImport: "pkg/cachecontrol", publicImport: "internal/store", wantServer: true},
		{name: "server subpackage", sdkImport: "internal/controller/client", wantServer: true},
		{name: "wire prefix is distinct", sdkImport: "internal/controllerwire"},
		{name: "SDK subpackage", sdkImport: "internal/store", wantServer: true},
		{name: "platform-only import", sdkImport: "internal/store", wantServer: true},
		{name: "tests excluded"},
		{name: "dependency load failure", sdkImport: "internal/missing", wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			production := "sparkwing/sdk.go"
			if tt.name == "SDK subpackage" {
				production = "sparkwing/docker/sdk.go"
			}
			if tt.name == "platform-only import" {
				t.Setenv("GOOS", "linux")
				t.Setenv("GOARCH", "amd64")
				production = "sparkwing/sdk_windows_arm64.go"
			}
			files := map[string]string{
				"go.mod":                               "module " + modulePath + "\n\ngo 1.26.0\n",
				"sparkwing/base.go":                    "package sdk\n",
				production:                             "package sdk\n",
				"sparkwing/sdk_test.go":                "package sdk\nimport _ \"" + modulePath + "/internal/store\"\n",
				"internal/store/store.go":              "package store\n",
				"internal/controller/client/client.go": "package client\n",
				"internal/controllerwire/wire.go":      "package controllerwire\n",
				"internal/sdkserve/serve.go":           "package sdkserve\n",
				"pkg/cachecontrol/cache.go":            "package cachecontrol\n",
			}
			for file, dependency := range map[string]string{
				production:                   tt.sdkImport,
				"internal/sdkserve/serve.go": tt.leafImport,
				"pkg/cachecontrol/cache.go":  tt.publicImport,
			} {
				if dependency != "" {
					files[file] += "import _ \"" + modulePath + "/" + dependency + "\"\n"
				}
			}
			for name, source := range files {
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			findings, err := sdkDependencyFindings(root)
			if tt.wantError {
				if err == nil {
					t.Fatal("unresolved dependency passed the guard")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (len(findings) > 0) != tt.wantServer {
				t.Fatalf("server dependency = %v, want %v: %s", findings, tt.wantServer, strings.Join(findings, "\n"))
			}
			if tt.name == "platform-only import" && (len(findings) != 1 || !strings.Contains(findings[0], "windows/arm64")) {
				t.Fatalf("expected only a Windows arm64 dependency finding, got %v", findings)
			}
		})
	}
}
