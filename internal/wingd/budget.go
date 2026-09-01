package wingd

import (
	"fmt"
	"strconv"
	"strings"
)

type Budget struct {
	Cores float64

	CoresFraction float64

	MemoryBytes uint64

	MemoryFraction float64

	Enforce bool

	IgnoreExternal bool

	Raw string
}

func (b Budget) IsSet() bool {
	return b.HasCap() || b.Enforce || b.IgnoreExternal
}

func (b Budget) HasCap() bool {
	return b.Cores > 0 || b.CoresFraction > 0 || b.MemoryBytes > 0 || b.MemoryFraction > 0
}

func (b Budget) Enforcing() bool {
	return b.Enforce && b.HasCap()
}

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

func parseByteSize(tok string) (bytes uint64, ok bool, err error) {
	t := strings.ToLower(strings.TrimSpace(tok))
	type unit struct {
		suf   string
		scale float64
	}
	units := []unit{
		{"tib", 1 << 40},
		{"tb", 1 << 40},
		{"t", 1 << 40},
		{"gib", 1 << 30},
		{"gb", 1 << 30},
		{"g", 1 << 30},
		{"mib", 1 << 20},
		{"mb", 1 << 20},
		{"m", 1 << 20},
		{"kib", 1 << 10},
		{"kb", 1 << 10},
		{"k", 1 << 10},
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
