package main

import (
	"strings"
	"testing"
)

func TestResolveOutputFormat(t *testing.T) {
	tests := []struct {
		name    string
		outFmt  string
		want    string
		wantErr string
	}{
		{name: "empty defaults to pretty", outFmt: "", want: "pretty"},
		{name: "pretty passes through", outFmt: "pretty", want: "pretty"},
		{name: "json passes through", outFmt: "json", want: "json"},
		{name: "plain passes through", outFmt: "plain", want: "plain"},
		{name: "invalid value errors", outFmt: "yaml", wantErr: `must be one of pretty|json|plain, got "yaml"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveOutputFormat(tc.outFmt, "test cmd")
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q; got %q with no error", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
