package nodemetrics

import "testing"

func TestParseProcessRSSKB(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want int64
		ok   bool
	}{
		{name: "ps row", out: "  123456\n", want: 123456 * 1024, ok: true},
		{name: "no trailing newline", out: "8", want: 8 * 1024, ok: true},
		{name: "empty", out: "", ok: false},
		{name: "blank lines only", out: "\n  \n", ok: false},
		{name: "garbage", out: "not-a-number\n", ok: false},
		{name: "zero", out: "0\n", ok: false},
		{name: "overflow", out: "9007199254740992", ok: false},
		{name: "largest KB", out: "9007199254740991", want: 9223372036854774784, ok: true},
		{name: "negative", out: "-4\n", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseProcessRSSKB(tc.out)
			if ok != tc.ok || got != tc.want {
				t.Errorf("parseProcessRSSKB(%q) = %d, %t; want %d, %t", tc.out, got, ok, tc.want, tc.ok)
			}
		})
	}
}
