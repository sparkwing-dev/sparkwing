package health

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDecodeReadsTheContractEveryServicePublishes(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		wantStatus   string
		wantProblems int
		wantAuth     string
		wantDegraded bool
		wantErr      bool
	}{
		{
			name:         "healthy",
			body:         `{"status":"ok"}`,
			wantStatus:   StatusOK,
			wantDegraded: false,
		},
		{
			name:         "controller degraded",
			body:         `{"status":"degraded","auth":"enabled","problems":["triggers: 3 claimed >30m without /done"]}`,
			wantStatus:   StatusDegraded,
			wantProblems: 1,
			wantAuth:     "enabled",
			wantDegraded: true,
		},
		{
			name:         "logs degraded",
			body:         `{"status":"degraded","problems":["root: disk free 412MiB (<1GiB) on /data"]}`,
			wantStatus:   StatusDegraded,
			wantProblems: 1,
			wantDegraded: true,
		},
		{
			name: "cache degraded",
			body: `{"status":"degraded","problems":["gitcache: background fetch failing for all 7 repos",` +
				`"proxy: cache directory not writable: permission denied"]}`,
			wantStatus:   StatusDegraded,
			wantProblems: 2,
			wantDegraded: true,
		},
		{
			name:         "problems without status",
			body:         `{"status":"ok","problems":["gitcache: background fetch failing"]}`,
			wantStatus:   StatusOK,
			wantProblems: 1,
			wantDegraded: true,
		},
		{
			name:         "healthy with auth disabled",
			body:         `{"status":"ok","auth":"disabled"}`,
			wantStatus:   StatusOK,
			wantAuth:     "disabled",
			wantDegraded: false,
		},
		{
			name:    "not the contract",
			body:    "OK\n",
			wantErr: true,
		},
		{
			name:    "empty body",
			body:    "",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Decode(strings.NewReader(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Decode(%q) = %+v, want an error", tc.body, got)
				}
				if got.Degraded() {
					t.Fatal("an undecodable body must not read as degraded")
				}
				return
			}
			if err != nil {
				t.Fatalf("Decode(%q): %v", tc.body, err)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if len(got.Problems) != tc.wantProblems {
				t.Errorf("Problems = %v, want %d", got.Problems, tc.wantProblems)
			}
			if got.Auth != tc.wantAuth {
				t.Errorf("Auth = %q, want %q", got.Auth, tc.wantAuth)
			}
			if got.Degraded() != tc.wantDegraded {
				t.Errorf("Degraded() = %v, want %v", got.Degraded(), tc.wantDegraded)
			}
		})
	}
}

func TestDecodeSeparatesAnUnreadableBodyFromOneOutsideTheContract(t *testing.T) {
	_, err := Decode(strings.NewReader("OK\n"))
	if !errors.Is(err, ErrNotContract) {
		t.Fatalf("plain-text body: err = %v, want ErrNotContract", err)
	}

	_, err = Decode(&errReader{prefix: `{"status":"deg`})
	if err == nil {
		t.Fatal("an unreadable body decoded without error")
	}
	if errors.Is(err, ErrNotContract) {
		t.Fatalf("read failure reported as ErrNotContract: %v", err)
	}
	if !strings.Contains(err.Error(), "read health body") {
		t.Errorf("err = %v, want it to name the failed read", err)
	}
}

type errReader struct{ prefix string }

func (r *errReader) Read(p []byte) (int, error) {
	if r.prefix != "" {
		n := copy(p, r.prefix)
		r.prefix = r.prefix[n:]
		return n, nil
	}
	return 0, io.ErrUnexpectedEOF
}

func TestDecodeBoundsTheBody(t *testing.T) {
	oversized := `{"status":"ok","problems":["` + strings.Repeat("x", MaxBodyBytes) + `"]}`
	if _, err := Decode(strings.NewReader(oversized)); err == nil {
		t.Fatal("an oversized body decoded; the read is not bounded")
	}
}
