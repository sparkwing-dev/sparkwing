package sparkwingcache

import "testing"

func TestBinURLEscapesEverySegment(t *testing.T) {
	s := &Store{baseURL: "http://cache"}
	for key, want := range map[string]string{
		"a#frag":       "http://cache/bin/a%23frag",
		"a?delete=1":   "http://cache/bin/a%3Fdelete=1",
		"%2e%2e%2fetc": "http://cache/bin/%252e%252e%252fetc",
		"x/y z":        "http://cache/bin/x/y%20z",
	} {
		if got := s.binURL(key); got != want {
			t.Errorf("binURL(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestBinURLDropsAKeysBinPrefix(t *testing.T) {
	s := &Store{baseURL: "http://cache"}
	for key, want := range map[string]string{
		"bin/abcd1234":            "http://cache/bin/abcd1234",
		"bin/abcd1234.sha256":     "http://cache/bin/abcd1234.sha256",
		"abcd1234":                "http://cache/bin/abcd1234",
		"binary-without-a-prefix": "http://cache/bin/binary-without-a-prefix",
	} {
		if got := s.binURL(key); got != want {
			t.Errorf("binURL(%q) = %q, want %q", key, got, want)
		}
	}
}
