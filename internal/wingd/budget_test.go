package wingd

import "testing"

func TestParseBudget_Forms(t *testing.T) {
	tests := []struct {
		in        string
		cores     float64
		coresFr   float64
		mem       uint64
		memFr     float64
		enforce   bool
		ignoreExt bool
		isSet     bool
	}{
		{in: ""},
		{in: "6", cores: 6, isSet: true},
		{in: "6cores", cores: 6, isSet: true},
		{in: "50%", coresFr: 0.5, isSet: true},
		{in: "6,8gb", cores: 6, mem: 8 << 30, isSet: true},
		{in: "50%,50%", coresFr: 0.5, memFr: 0.5, isSet: true},
		{in: "6,8gib,enforce", cores: 6, mem: 8 << 30, enforce: true, isSet: true},
		{in: "enforce,4", cores: 4, enforce: true, isSet: true},
		{in: "512mb", mem: 512 << 20, isSet: true},
		{in: "ignore-external", ignoreExt: true, isSet: true},
		{in: "IGNORE-EXTERNAL", ignoreExt: true, isSet: true},
		{in: "6,ignore-external", cores: 6, ignoreExt: true, isSet: true},
		{in: "50%,8gb,enforce,ignore-external", coresFr: 0.5, mem: 8 << 30, enforce: true, ignoreExt: true, isSet: true},
		{in: "ignore-external,6", cores: 6, ignoreExt: true, isSet: true},
	}
	for _, tc := range tests {
		b, err := ParseBudget(tc.in)
		if err != nil {
			t.Errorf("ParseBudget(%q): unexpected error %v", tc.in, err)
			continue
		}
		if b.Cores != tc.cores || b.CoresFraction != tc.coresFr ||
			b.MemoryBytes != tc.mem || b.MemoryFraction != tc.memFr ||
			b.Enforce != tc.enforce || b.IgnoreExternal != tc.ignoreExt {
			t.Errorf("ParseBudget(%q) = %+v, want cores=%v coresFr=%v mem=%v memFr=%v enforce=%v ignoreExt=%v",
				tc.in, b, tc.cores, tc.coresFr, tc.mem, tc.memFr, tc.enforce, tc.ignoreExt)
		}
		if b.IsSet() != tc.isSet {
			t.Errorf("ParseBudget(%q).IsSet() = %v, want %v", tc.in, b.IsSet(), tc.isSet)
		}
	}
}

func TestParseBudget_Invalid(t *testing.T) {
	for _, in := range []string{"nonsense", "150%", "-2", "0", "6,7,8"} {
		if _, err := ParseBudget(in); err == nil {
			t.Errorf("ParseBudget(%q): expected error, got nil", in)
		}
	}
}

func TestBudget_CapCores(t *testing.T) {
	tests := []struct {
		budget  Budget
		machine float64
		want    float64
	}{
		{Budget{}, 10, 10},
		{Budget{Cores: 6}, 10, 6},
		{Budget{Cores: 20}, 10, 10},
		{Budget{CoresFraction: 0.5}, 10, 5},
	}
	for _, tc := range tests {
		if got := tc.budget.CapCores(tc.machine); got != tc.want {
			t.Errorf("CapCores(%+v, %v) = %v, want %v", tc.budget, tc.machine, got, tc.want)
		}
	}
}

func TestBudget_CapMemory(t *testing.T) {
	const machine = uint64(16) << 30
	if got := (Budget{MemoryBytes: 8 << 30}).CapMemory(machine); got != 8<<30 {
		t.Errorf("CapMemory abs = %d, want %d", got, uint64(8)<<30)
	}
	if got := (Budget{MemoryBytes: 32 << 30}).CapMemory(machine); got != machine {
		t.Errorf("CapMemory over-machine = %d, want %d (machine)", got, machine)
	}
	if got := (Budget{MemoryFraction: 0.5}).CapMemory(machine); got != machine/2 {
		t.Errorf("CapMemory fraction = %d, want %d", got, machine/2)
	}
}

func TestCgroupLimitLines(t *testing.T) {
	if got := cpuMaxLine(2); got != "200000 100000" {
		t.Errorf("cpuMaxLine(2) = %q, want %q", got, "200000 100000")
	}
	if got := cpuMaxLine(0.5); got != "50000 100000" {
		t.Errorf("cpuMaxLine(0.5) = %q, want %q", got, "50000 100000")
	}
	if got := cpuMaxLine(0); got != "max 100000" {
		t.Errorf("cpuMaxLine(0) = %q, want %q", got, "max 100000")
	}
	if got := memMaxLine(8 << 30); got != "8589934592" {
		t.Errorf("memMaxLine = %q, want %q", got, "8589934592")
	}
	if got := memMaxLine(0); got != "max" {
		t.Errorf("memMaxLine(0) = %q, want %q", got, "max")
	}
}
