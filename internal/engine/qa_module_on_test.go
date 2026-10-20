//go:build qatrust

package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestQATrustBuildRoutesOnlyQAPinsThroughTheQAProxy(t *testing.T) {
	dir := t.TempDir()
	written := filepath.Join(dir, "written.env")
	if err := os.WriteFile(written, []byte("GOPROXY=https://corp.example/proxy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	qa := Pin{SDK: GoSDK, Version: "v0.68.0-qa.3"}
	for _, tc := range []struct {
		name     string
		pin      Pin
		settings []string
		want     []string
	}{
		{"release pin", Pin{SDK: GoSDK, Version: "v0.68.0"}, nil, nil},
		{"replacement", Pin{SDK: GoSDK, Version: "v0.68.0-qa.3", Replacement: "../sdk"}, nil, nil},
		{"defaults", qa, nil, []string{"GOPROXY=https://qa-releases.sparkwing.dev/goproxy|https://proxy.golang.org,direct", "GONOSUMDB=github.com/sparkwing-dev/sparkwing"}},
		{"go env -w", qa, []string{"GOENV=" + written}, []string{"GOPROXY=https://qa-releases.sparkwing.dev/goproxy|https://corp.example/proxy", "GONOSUMDB=github.com/sparkwing-dev/sparkwing"}},
		{"GOPRIVATE seeds the exemption", qa, []string{"GOPRIVATE=example.com/private"}, []string{"GOPROXY=https://qa-releases.sparkwing.dev/goproxy|https://proxy.golang.org,direct", "GONOSUMDB=example.com/private,github.com/sparkwing-dev/sparkwing"}},
		{"explicit GONOSUMDB", qa, []string{"GOPRIVATE=example.com/private", "GONOSUMDB=example.com/other"}, []string{"GOPROXY=https://qa-releases.sparkwing.dev/goproxy|https://proxy.golang.org,direct", "GONOSUMDB=example.com/other,github.com/sparkwing-dev/sparkwing"}},
		{"offline", qa, []string{"GOPROXY=off"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := QAModuleEnvironment(tc.pin, func(args ...string) ([]byte, error) {
				command := exec.Command("go", args...)
				command.Dir, command.Env = dir, append(goEnvironment(dir), tc.settings...)
				return command.Output()
			})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func goEnvironment(dir string) []string {
	var environment []string
	for _, setting := range os.Environ() {
		name, _, _ := strings.Cut(setting, "=")
		switch name {
		case "GOPROXY", "GONOSUMDB", "GOPRIVATE", "GONOPROXY", "GOFLAGS", "GOENV":
			continue
		}
		environment = append(environment, setting)
	}
	return append(environment, "GOENV="+filepath.Join(dir, "missing.env"))
}
