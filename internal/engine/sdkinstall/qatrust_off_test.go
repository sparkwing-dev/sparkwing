//go:build !qatrust

package sdkinstall

import "testing"

func TestDefaultBuildLeavesTheModuleProxyToTheCaller(t *testing.T) {
	if got := qaModuleEnvironment([]string{"GOPROXY=https://proxy.example"}); len(got) != 0 {
		t.Fatalf("default build sets %v", got)
	}
}
