package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUpgradeResolvesQASDKPinsThroughTheQAProxyOnlyInQATrustBuilds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake go tool is a shell script")
	}
	tools := t.TempDir()
	fake := "#!/bin/sh\nif [ \"$1\" = env ]; then echo '{\"GOPROXY\":\"https://proxy.golang.org,direct\",\"GONOSUMDB\":\"\",\"GOPRIVATE\":\"\"}'; exit 0; fi\necho \"GOPROXY=$GOPROXY\"\nexit 7\n"
	if err := os.WriteFile(filepath.Join(tools, "go"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOPROXY", "")
	err := (&Resolver{}).nativeCommand(t.Context(), t.TempDir(), Pin{SDK: GoSDK, Version: "v0.68.0-qa.5"}, "go", "mod", "tidy")
	if err == nil {
		t.Fatal("fake go mod tidy succeeded")
	}
	routed := strings.Contains(err.Error(), "GOPROXY=https://qa-releases.sparkwing.dev/goproxy|https://proxy.golang.org,direct")
	if routed != QATrust {
		t.Fatalf("QATrust=%v but go mod tidy saw: %v", QATrust, err)
	}
}
