package jobs

import "testing"

func TestExecutableFormatNamesTheContainer(t *testing.T) {
	cases := map[string][]byte{
		"ELF":    {0x7f, 'E', 'L', 'F', 2, 1},
		"Mach-O": {0xcf, 0xfa, 0xed, 0xfe, 7, 0},
		"PE":     {'M', 'Z', 0x90, 0x00},
	}
	for want, head := range cases {
		if got := executableFormat(head); got != want {
			t.Errorf("%v: got %q, want %q", head, got, want)
		}
	}
	for _, head := range [][]byte{nil, {'#', '!', '/', 'b'}, {'p', 'a', 'c', 'k'}, {0x1f, 0x8b, 8, 0}, {'M'}} {
		if got := executableFormat(head); got != "" {
			t.Errorf("%v: got %q, want plain file", head, got)
		}
	}
}
