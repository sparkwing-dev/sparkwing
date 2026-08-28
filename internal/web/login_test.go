package web

import "testing"

func TestSafeNext(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "/"},
		{"/", "/"},
		{"/runs", "/runs"},
		{"/pipelines/foo?x=1", "/pipelines/foo?x=1"},
		{"/runs?run=x&tab=logs", "/runs?run=x&tab=logs"},
		{"//evil.com/foo", "/"},
		{"//evil.com", "/"},
		{`/\evil.com`, "/"},
		{`/safe\evil.com`, "/"},
		{"/%2f%2fevil.com", "/"},
		{"/%5cevil.com", "/"},
		{"/runs#fragment", "/"},
		{"/runs\nmalformed", "/"},
		{"https://evil.com", "/"},
		{"http://evil.com", "/"},
		{"javascript:alert(1)", "/"},
		{"runs", "/"},
		{"../etc", "/"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := safeNext(tc.in); got != tc.want {
				t.Fatalf("safeNext(%q)=%q want %q", tc.in, got, tc.want)
			}
		})
	}
}
