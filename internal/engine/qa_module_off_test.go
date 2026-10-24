//go:build !qatrust

package engine

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

func TestDefaultBuildLeavesTheModuleProxyToTheCaller(t *testing.T) {
	got, err := QAModuleEnvironment(engineid.Pin{SDK: engineid.GoSDK, Version: "v0.68.0-qa.3"}, func(...string) ([]byte, error) {
		t.Fatal("default build ran go")
		return nil, nil
	})
	if err != nil || len(got) != 0 {
		t.Fatalf("default build sets %v, %v", got, err)
	}
}
