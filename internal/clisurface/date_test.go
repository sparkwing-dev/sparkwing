package clisurface

import (
	"testing"
	"time"
)

func TestParseLooseDate_RecognizedForms(t *testing.T) {
	cases := []string{"today", "yesterday", "24h", "7d", "5m", "2026-05-01"}
	for _, c := range cases {
		if _, err := ParseLooseDate(c); err != nil {
			t.Errorf("%q failed: %v", c, err)
		}
	}
}

func TestParseLooseDate_RejectsGarbage(t *testing.T) {
	if _, err := ParseLooseDate("not-a-date"); err == nil {
		t.Errorf("expected error for garbage input")
	}
}

func TestParseLooseDurationRejectsScaledOverflow(t *testing.T) {
	for _, raw := range []string{"106752d", "-106752d", "15251w", "-15251w"} {
		t.Run(raw, func(t *testing.T) {
			if got, err := ParseLookback(raw); err == nil {
				t.Fatalf("ParseLooseDuration(%q) = %s, want overflow error", raw, got)
			}
		})
	}
}

func TestParseLooseDurationAcceptsUppercaseUnits(t *testing.T) {
	for raw, want := range map[string]time.Duration{"7D": 7 * 24 * time.Hour, "2W": 14 * 24 * time.Hour, "3H": 3 * time.Hour, "5MS": 5 * time.Millisecond} {
		got, err := ParseLookback(raw)
		if err != nil || got != want {
			t.Errorf("%s: got %s, %v; want %s", raw, got, err, want)
		}
	}
}
