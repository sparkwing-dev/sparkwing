//go:build unix

package orchestrator

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRecordRunProfile_CommandCPUIsStoredWithoutChangingIntervalRates(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "sh", "-c", `i=0; while [ "$i" -lt 100000 ]; do i=$((i+1)); done`)
	started := time.Now()
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	wall := time.Since(started)
	usage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	if !ok || usage == nil {
		t.Fatal("child exit supplied no resource usage")
	}
	cpu := time.Duration(usage.Utime.Nano()) + time.Duration(usage.Stime.Nano())
	if cpu <= 0 {
		t.Fatalf("child CPU = %s; workload must consume measurable CPU", cpu)
	}
	for _, withCommand := range []bool{false, true} {
		st, start := seedUsageRun(t, "command-native", []usageNode{{id: "build", dur: 10 * time.Second, wall: 10 * time.Second, samples: ticks(5, 200, 400)}})
		if withCommand {
			if err := st.AddNodeMetricSample(t.Context(), "r1", "build", store.MetricSample{
				Kind: store.MetricCommand, TS: start.Add(9 * time.Second), CPUTime: cpu,
				CPUMillicores: int64(cpu.Seconds() / wall.Seconds() * 1000),
			}); err != nil {
				t.Fatal(err)
			}
			samples, err := st.ListNodeMetrics(t.Context(), "r1", "build")
			if err != nil || len(samples) != 6 {
				t.Fatalf("samples=%+v, %v", samples, err)
			}
			if samples[5].Kind != store.MetricCommand || samples[5].CPUTime != cpu {
				t.Fatalf("command=%+v; want child CPU %s", samples[5], cpu)
			}
		}
		recordRunProfile(t.Context(), localState{st: st}, "command-native", "r1", nil, "", runCharge{}, false, start, start.Add(10*time.Second))
		for _, node := range []string{"", "build"} {
			profile, err := st.GetPipelineProfile(t.Context(), "command-native", node)
			if err != nil {
				t.Fatal(err)
			}
			if profile == nil || profile.SampleCount != 1 || profile.PeakCores != 0.2 || profile.SustainedCores != 0.2 {
				t.Fatalf("command=%v %q profile=%+v; want one observation with 0.2 sampled peak and sustained cores", withCommand, node, profile)
			}
		}
	}
}
