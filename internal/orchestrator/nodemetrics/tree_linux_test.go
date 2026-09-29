package nodemetrics

import (
	"strings"
	"testing"
	"time"
)

func TestProcessStatCPUUnitsAndIdentity(t *testing.T) {
	const raw = "42 (a process (with) spaces) S 1 1 1 0 -1 0 0 0 0 0 150 50 25 75 20 0 1 0 123 0 0"
	p, zombie, ok := parseProcessStat(raw, 42, 100)
	if !ok || !p.valid || zombie || p.parent != 1 || p.birth != 123 || p.cpu != 2*time.Second || p.reaped != time.Second {
		t.Fatalf("parsed stat = %+v, zombie=%v, ok=%v", p, zombie, ok)
	}
	_, zombie, ok = parseProcessStat(strings.Replace(raw, ") S ", ") Z ", 1), 42, 100)
	if !ok || !zombie {
		t.Fatal("zombie state lost")
	}
	for _, broken := range []string{
		"",
		"42 (short) S 1",
		strings.Replace(raw, "42 (", "43 (", 1),
		strings.Replace(raw, "42 (", "bad (", 1),
		strings.Replace(raw, "150 50", "-1 50", 1),
		strings.Replace(raw, "150 50", "18446744073709551615 50", 1),
		strings.Replace(raw, "150 50", "9223372036854775807 0", 1),
		strings.Replace(raw, "25 75", "-1 75", 1),
		strings.Replace(raw, "123 0 0", "-1 0 0", 1),
	} {
		if _, _, ok := parseProcessStat(broken, 42, 100); ok {
			t.Fatalf("malformed stat accepted: %q", broken)
		}
	}
	if _, _, ok := parseProcessStat(raw, 42, 0); ok {
		t.Fatal("missing clock frequency accepted")
	}
}

func TestProcessResidentBytes(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int64
		ok   bool
	}{
		{"Rss: 64 kB\nPss: 32 kB\n", 65536, true},
		{"Rss: 0 kB\n", 0, true},
		{"Pss: 64 kB\n", 0, false},
		{"Rss: 64 bytes\n", 0, false},
		{"Rss: -1 kB\n", 0, false},
		{"Rss: 9223372036854775807 kB\n", 0, false},
		{"Rss: 64 kB\nRss: 64 kB\n", 0, false},
		{"Rss: 64\n", 0, false},
	} {
		got, ok := processResidentBytes(tc.raw)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("RSS %q = %d,%v; want %d,%v", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}
