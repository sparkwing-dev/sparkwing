package k8s

import (
	"math"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestPodMemoryIntegers(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		memory, ceiling, request, limit int64
	}{
		{"fractional byte", 5, 0, 5, 6},
		{"large odd bytes", 9007199254740993, 0, 9007199254740993, 11258999068426241},
		{"maximum bytes", math.MaxInt64, 0, math.MaxInt64, math.MaxInt64},
		{"below limit", 7378697629483820645, 0, 7378697629483820645, math.MaxInt64 - 1},
		{"exact limit", 7378697629483820646, 0, 7378697629483820646, math.MaxInt64},
		{"above limit", 7378697629483820647, 0, 7378697629483820647, math.MaxInt64},
		{"ceiling", math.MaxInt64, 1024, 1024, 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultsCfg
			cfg.MemoryCeiling = tc.ceiling
			got := podResources(capacity.Resolution{MemoryBytes: tc.memory, Source: store.CostSourceMeasured}, cfg)
			if request, limit := bytesOf(got.Requests[corev1.ResourceMemory]), bytesOf(got.Limits[corev1.ResourceMemory]); request != tc.request || limit != tc.limit {
				t.Errorf("request/limit = %d/%d, want %d/%d", request, limit, tc.request, tc.limit)
			}
		})
	}
}
