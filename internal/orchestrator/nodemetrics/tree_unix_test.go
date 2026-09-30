//go:build linux || darwin

package nodemetrics

import (
	"os"
	"testing"
)

func TestProcessTreeUsageReadsCurrentProcess(t *testing.T) {
	got := processTreeUsage(os.Getpid())
	if !got.valid || got.cpu < 0 || got.memory <= 0 {
		t.Fatalf("current process usage = %+v", got)
	}
}
