package storage_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

func TestSafeSegment_RejectsTraversalAndSeparators(t *testing.T) {
	for _, bad := range []string{
		"", ".", "..", "a/b", `a\b`, "../etc", "run\x00id", "x\ny",
	} {
		if err := storage.SafeSegment(bad); err == nil {
			t.Errorf("SafeSegment(%q) = nil, want error", bad)
		}
	}
	for _, ok := range []string{
		"run-123", "node.build", "r1_n2", "UPPER", "with space", "ünïcode", "a..b",
	} {
		if err := storage.SafeSegment(ok); err != nil {
			t.Errorf("SafeSegment(%q) = %v, want nil", ok, err)
		}
	}
}
