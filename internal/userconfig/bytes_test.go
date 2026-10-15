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
