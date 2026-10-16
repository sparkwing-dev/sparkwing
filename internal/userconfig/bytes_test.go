package userconfig

import "testing"

func TestParseByteSize(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"1024", 1024},
		{"0", 0},
		{"512B", 512},
		{"2KiB", 2048},
		{"1MiB", 1 << 20},
		{"2GiB", 2 << 30},
		{"1KB", 1000},
		{"1MB", 1000 * 1000},
		{" 4GiB ", 4 << 30},
		{"1.5GiB", 1610612736},
	} {
		got, err := ParseByteSize(tc.in)
		if err != nil {
			t.Errorf("ParseByteSize(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseByteSize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"", "-1", "banana", "12PB", "-2GiB", "NaNGB", "+InfGB", "9223372036854775808B"} {
		if _, err := ParseByteSize(bad); err == nil {
			t.Errorf("ParseByteSize(%q) should have failed", bad)
		}
	}
}

func TestByteBoundDisablesOnlyForExplicitZero(t *testing.T) {
	for _, raw := range []string{"0", "0B", "0.0KiB", "0e-999B"} {
		if got, err := ParseByteSize(raw); err != nil || got != 0 {
			t.Errorf("explicit zero %q = %d, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"0.5B", "0.0001KiB", "1e-999B", "0x1p-999B"} {
		if got, err := ParseByteSize(raw); err == nil {
			t.Errorf("positive bound %q became %d without an error", raw, got)
		}
	}
	for _, raw := range []string{"9223372036854775807B", "9223372036854775807.1B"} {
		got, err := ParseByteSize(raw)
		if raw == "9223372036854775807B" && (err != nil || got != 9223372036854775807) {
			t.Errorf("representable bound = %d, %v", got, err)
		}
		if raw != "9223372036854775807B" && err == nil {
			t.Error("bound above int64 maximum accepted")
		}
	}
}
