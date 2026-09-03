package wingd

import (
	"testing"
	"time"
)

const procStatIOWaitHeavy = "cpu  60 0 40 400 500 0 0 0 0 0\ncpu0 60 0 40 400 500 0 0 0\nintr 12345\n"

func TestParseProcStatCPU_ExcludesIdleAndIOWaitFromBusy(t *testing.T) {
	t.Parallel()
	totals, ok := parseProcStatCPU(procStatIOWaitHeavy)
	if !ok {
		t.Fatal("the aggregate cpu line is present; parse reported it missing")
	}
	if totals.busy != 100 {
		t.Errorf("busy = %v, want 100 (user 60 + system 40); idle and iowait retire no instructions", totals.busy)
	}
	if totals.total != 1000 {
		t.Errorf("total = %v, want 1000 across all eight fields", totals.total)
	}
}

func TestParseProcStatCPU_RejectsAbsentAndShortLines(t *testing.T) {
	t.Parallel()
	for name, data := range map[string]string{
		"no cpu line":  "intr 12345\nctxt 99\n",
		"per-cpu only": "cpu0 60 0 40 400 500 0 0 0\n",
		"short":        "cpu  60 0 40 400\n",
		"empty":        "",
	} {
		if _, ok := parseProcStatCPU(data); ok {
			t.Errorf("%s: reported a reading from input carrying none", name)
		}
	}
}

func TestParseProcStatCPU_RejectsNonNumericField(t *testing.T) {
	t.Parallel()
	if _, ok := parseProcStatCPU("cpu  60 0 xx 400 500 0 0 0\n"); ok {
		t.Error("a malformed tick field must fail the reading, not contribute zero")
	}
}

func TestBusyCoresFromTotals_ScalesTheBusyShareAcrossCores(t *testing.T) {
	t.Parallel()
	prev := cpuTotals{busy: 100, total: 1000}
	cur := cpuTotals{busy: 350, total: 2000}
	got, ok := busyCoresFromTotals(prev, cur, 8)
	if !ok {
		t.Fatal("a forward-moving span is a measurement")
	}
	if got != 2 {
		t.Errorf("busy cores = %v, want 2", got)
	}
}

func TestBusyCoresFromTotals_RejectsSpansThatMeasureNothing(t *testing.T) {
	t.Parallel()
	base := cpuTotals{busy: 100, total: 1000}
	for name, tc := range map[string]struct{ prev, cur cpuTotals }{
		"repeated reading": {base, base},
		"counter reset":    {base, cpuTotals{busy: 10, total: 100}},
		"busy went back":   {base, cpuTotals{busy: 90, total: 2000}},
	} {
		if _, ok := busyCoresFromTotals(tc.prev, tc.cur, 8); ok {
			t.Errorf("%s: reported a measurement; an idle machine looks identical and must not be assumed", name)
		}
	}
}

func TestBusyCoresFromTotals_ClampsToTheMachine(t *testing.T) {
	t.Parallel()
	got, ok := busyCoresFromTotals(cpuTotals{}, cpuTotals{busy: 1000, total: 1000}, 4)
	if !ok {
		t.Fatal("a fully busy span is a measurement")
	}
	if got != 4 {
		t.Errorf("busy cores = %v, want the 4-core capacity", got)
	}
}

func TestProcessCPUFraction_RequiresTheSameProcessIdentity(t *testing.T) {
	t.Parallel()
	before := time.Unix(100, 0)
	for name, identity := range map[string][2]uint64{
		"recycled PID":     {1000, 2000},
		"unknown identity": {0, 0},
	} {
		previous := cpuSample{cpuSeconds: 1, at: before, startTicks: identity[0]}
		current := cpuSample{cpuSeconds: 3, at: before.Add(time.Second), startTicks: identity[1]}
		if usage, measured := processCPUFraction(previous, current); measured || usage != 0 {
			t.Errorf("%s CPU = %v, measured %v; want a fresh unmeasured baseline", name, usage, measured)
		}
	}
}

func TestProcessCPUFraction_MeasuresAnUnchangedIdentity(t *testing.T) {
	t.Parallel()
	before := time.Unix(100, 0)
	previous := cpuSample{cpuSeconds: 1, at: before, startTicks: 1000}
	current := cpuSample{cpuSeconds: 3, at: before.Add(time.Second), startTicks: 1000}
	if usage, measured := processCPUFraction(previous, current); !measured || usage != 2 {
		t.Fatalf("process CPU = %v, measured %v; want two cores", usage, measured)
	}
}

func TestSumProcessCPUPercent_SumsRowsIntoCores(t *testing.T) {
	t.Parallel()
	got, ok := sumProcessCPUPercent(" 99.5\n  0.0\n 45.5\n 55.0\n", 8)
	if !ok {
		t.Fatal("four parsable rows are a reading")
	}
	if got != 2 {
		t.Errorf("busy cores = %v, want 2 from 200%% of one core", got)
	}
}

func TestSumProcessCPUPercent_RejectsOutputWithNoRow(t *testing.T) {
	t.Parallel()
	for name, out := range map[string]string{
		"empty":      "",
		"whitespace": "\n  \n\t\n",
		"unparsable": "ps: command failed\n",
	} {
		if _, ok := sumProcessCPUPercent(out, 8); ok {
			t.Errorf("%s: a process table that could not be listed must not read as an idle machine", name)
		}
	}
}

func TestSumProcessCPUPercent_SkipsUnparsableRowAmongGoodOnes(t *testing.T) {
	t.Parallel()
	got, ok := sumProcessCPUPercent("100.0\n<defunct>\n100.0\n", 8)
	if !ok {
		t.Fatal("two parsable rows are a reading")
	}
	if got != 2 {
		t.Errorf("busy cores = %v, want 2; the process table races the processes in it", got)
	}
}

func TestSumProcessCPUPercent_ClampsToTheMachine(t *testing.T) {
	t.Parallel()
	got, ok := sumProcessCPUPercent("400.0\n400.0\n", 2)
	if !ok {
		t.Fatal("two parsable rows are a reading")
	}
	if got != 2 {
		t.Errorf("busy cores = %v, want the 2-core capacity", got)
	}
}
