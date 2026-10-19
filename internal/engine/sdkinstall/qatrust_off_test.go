//go:build !qatrust

package sdkinstall

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

func TestDefaultBuildLeavesTheModuleProxyToTheCaller(t *testing.T) {
	got, err := qaModuleEnvironment(t.Context(), t.TempDir(), engine.Pin{SDK: engine.GoSDK, Version: "v0.68.0-qa.3"}, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("default build sets %v, %v", got, err)
	}
}
