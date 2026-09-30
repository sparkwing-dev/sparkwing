package k8s

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestPodResources_MemorySurvivesDefaultCPU(t *testing.T) {
	for _, tc := range []struct {
		name                                    string
		memory, ceiling, wantRequest, wantLimit int64
	}{
		{"known memory", 512 << 20, 0, 512 << 20, 640 << 20},
		{"memory ceiling", 512 << 20, 256 << 20, 256 << 20, 256 << 20},
		{"default memory", 0, 0, 128 << 20, 2 << 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultsCfg
			cfg.MemoryCeiling = tc.ceiling
			res := capacity.Resolution{Cores: 4, MemoryBytes: tc.memory, Source: store.CostSourceDefault}
			got := podResources(res, cfg)
			if bytesOf(got.Requests[corev1.ResourceMemory]) != tc.wantRequest || bytesOf(got.Limits[corev1.ResourceMemory]) != tc.wantLimit {
				t.Errorf("memory with default CPU: got %+v; want request %d and limit %d bytes", got, tc.wantRequest, tc.wantLimit)
			}
			if milli(got.Requests[corev1.ResourceCPU]) != 100 || milli(got.Limits[corev1.ResourceCPU]) != 2000 {
				t.Errorf("memory changed the default CPU policy: %+v", got)
			}
		})
	}
}
