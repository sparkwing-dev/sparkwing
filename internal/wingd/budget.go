package wingd

import (
	"fmt"
	"strconv"
	"strings"
)

// Budget is the operator's cap on how much of the machine sparkwing may
// use. It caps the admission ledger's host capacity below the machine
// total and, when Enforce is set, opts into OS-level hardening (a cgroup
// on Linux, background scheduling on macOS). A fraction and an absolute
// on the same dimension cannot both be set; the parser rejects that.
type Budget struct {
	// Cores caps host CPU to an absolute core count. Zero means unset.
	Cores float64
	// CoresFraction caps host CPU to a share (0,1] of the machine's cores.
	// Zero means unset.
	CoresFraction float64
	// MemoryBytes caps host memory to an absolute byte count. Zero means
	// unset.
	MemoryBytes uint64
	// MemoryFraction caps host memory to a share (0,1] of machine memory.
	// Zero means unset.
	MemoryFraction float64
	// Enforce opts into OS-level hardening of the budget in addition to
	// capping admission.
	Enforce bool
	// IgnoreExternal makes admission stop subtracting measured non-sparkwing
	// load from its headroom -- the operator's escape hatch for a misreading
	// host sensor. Contention detection keeps using the real saturation, so
	// observability stays truthful. It is usable with or without a numeric
	// budget.
	IgnoreExternal bool
	// Raw is the setting string as supplied, for display.
	Raw string
}

// IsSet reports whether the budget caps any dimension, requests
// enforcement, or ignores external load.
func (b Budget) IsSet() bool {
	return b.HasCap() || b.Enforce || b.IgnoreExternal
}

// HasCap reports whether the budget caps cores or memory.
func (b Budget) HasCap() bool {
	return b.Cores > 0 || b.CoresFraction > 0 || b.MemoryBytes > 0 || b.MemoryFraction > 0
}

// Enforcing reports whether OS-level hardening should be applied: the
// budget opted into enforcement and actually caps a dimension to enforce.
func (b Budget) Enforcing() bool {
	return b.Enforce && b.HasCap()
}

// CapCores resolves the budget's core cap against a machine total. It
// returns the machine total unchanged when no core cap is set, and never
// a value above the machine total.
func (b Budget) CapCores(machine float64) float64 {
	limit := machine
	if b.CoresFraction > 0 {
		limit = b.CoresFraction * machine
	} else if b.Cores > 0 {
		limit = b.Cores
	}
	if limit > machine {
		return machine
	}
	if limit < 0 {
		return 0
	}
	return limit
}

// CapMemory resolves the budget's memory cap against a machine total. It
// returns the machine total unchanged when no memory cap is set, and
// never a value above the machine total.
func (b Budget) CapMemory(machine uint64) uint64 {
	limit := machine
	switch {
	case b.MemoryFraction > 0:
		limit = uint64(b.MemoryFraction * float64(machine))
	case b.MemoryBytes > 0:
		limit = b.MemoryBytes
	}
	if limit > machine {
		return machine
	}
	return limit
}

// humanBytesLog renders a byte count in the largest binary unit that
// keeps it readable, for the daemon's operational log lines.
func humanBytesLog(v uint64) string {
	const unit = 1024.0
	f := float64(v)
	if f < unit {
		return fmt.Sprintf("%dB", v)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	n := f
	i := -1
	for n >= unit && i < len(units)-1 {
		n /= unit
		i++
	}
	return fmt.Sprintf("%.1f%s", n, units[i])
}

// ParseBudget parses a machine-budget setting. The value is a comma-
// separated list of at most one cores term, one memory term, and the
// optional literal "enforce":
//
//	6            6 cores
//	50%          half the machine's cores
//	6,8gb        6 cores and 8 GiB of memory
//	50%,50%      half the cores and half the memory
//	6,8gb,enforce  the above, plus OS-level hardening
//	ignore-external  ignore measured non-sparkwing load in admission
//
// A cores term is a bare number (optionally suffixed c/core/cores) or a
// percent; the first percent or plain number is read as cores, a second
// as memory. A memory term carries a byte-size suffix (kb/mb/gb/tb or the
// ib/bare forms). The literals "enforce" and "ignore-external" are option
// terms usable alone or alongside a cap. An empty string yields a zero
// Budget with no error.
func ParseBudget(s string) (Budget, error) {
	b := Budget{Raw: strings.TrimSpace(s)}
	if b.Raw == "" {
		return b, nil
	}
	coresSet := false
	for _, tok := range strings.Split(b.Raw, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if strings.EqualFold(tok, "enforce") {
			b.Enforce = true
			continue
		}
		if strings.EqualFold(tok, "ignore-external") {
			b.IgnoreExternal = true
			continue
		}
		if strings.HasSuffix(tok, "%") {
			f, err := parsePercent(tok)
			if err != nil {
				return Budget{}, err
			}
			if !coresSet {
				b.CoresFraction = f
				coresSet = true
			} else {
				b.MemoryFraction = f
			}
			continue
		}
		if bytes, ok, err := parseByteSize(tok); err != nil {
			return Budget{}, err
		} else if ok {
			b.MemoryBytes = bytes
			continue
		}
		cores, err := parseCoreCount(tok)
		if err != nil {
			return Budget{}, err
		}
		if coresSet {
			return Budget{}, fmt.Errorf("budget %q: cores set twice; give at most one cores term", s)
		}
		b.Cores = cores
		coresSet = true
	}
	return b, nil
}

// parsePercent parses "NN%" into a fraction in (0,1].
func parsePercent(tok string) (float64, error) {
	n, err := strconv.ParseFloat(strings.TrimSuffix(tok, "%"), 64)
	if err != nil {
		return 0, fmt.Errorf("budget: %q is not a percentage", tok)
	}
	if n <= 0 || n > 100 {
		return 0, fmt.Errorf("budget: percentage %q out of range; want (0, 100]", tok)
	}
	return n / 100, nil
}

// parseCoreCount parses a positive core count, tolerating a c/core/cores
// suffix.
func parseCoreCount(tok string) (float64, error) {
	t := strings.ToLower(tok)
	for _, suf := range []string{"cores", "core", "c"} {
		if strings.HasSuffix(t, suf) {
			t = strings.TrimSuffix(t, suf)
			break
		}
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
	if err != nil {
		return 0, fmt.Errorf("budget: %q is not a core count, percentage, or memory size", tok)
	}
	if n <= 0 {
		return 0, fmt.Errorf("budget: cores %q must be positive", tok)
	}
	return n, nil
}

// parseByteSize parses a byte-size term (e.g. "8gb", "512mib"). ok is
// false when tok carries no recognized size suffix, so the caller can try
// it as a core count instead.
func parseByteSize(tok string) (bytes uint64, ok bool, err error) {
	t := strings.ToLower(strings.TrimSpace(tok))
	type unit struct {
		suf   string
		scale float64
	}
	units := []unit{
		{"tib", 1 << 40}, {"tb", 1 << 40}, {"t", 1 << 40},
		{"gib", 1 << 30}, {"gb", 1 << 30}, {"g", 1 << 30},
		{"mib", 1 << 20}, {"mb", 1 << 20}, {"m", 1 << 20},
		{"kib", 1 << 10}, {"kb", 1 << 10}, {"k", 1 << 10},
	}
	for _, u := range units {
		if !strings.HasSuffix(t, u.suf) {
			continue
		}
		num := strings.TrimSpace(strings.TrimSuffix(t, u.suf))
		n, perr := strconv.ParseFloat(num, 64)
		if perr != nil {
			return 0, false, fmt.Errorf("budget: %q is not a memory size", tok)
		}
		if n <= 0 {
			return 0, false, fmt.Errorf("budget: memory %q must be positive", tok)
		}
		return uint64(n * u.scale), true, nil
	}
	return 0, false, nil
}
