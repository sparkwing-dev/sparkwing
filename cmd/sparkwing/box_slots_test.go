package main

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/boxslot"
)

func TestApplyBoxSlotControl(t *testing.T) {
	dir := t.TempDir()

	if err := applyBoxSlotControl(dir, "4"); err != nil {
		t.Fatalf("set 4: %v", err)
	}
	if v, _, _ := boxslot.ReadControl(dir); v != "4" {
		t.Fatalf("control = %q, want 4", v)
	}

	if err := applyBoxSlotControl(dir, "OFF"); err != nil {
		t.Fatalf("set OFF: %v", err)
	}
	if v, _, _ := boxslot.ReadControl(dir); v != "off" {
		t.Fatalf("control = %q, want off", v)
	}

	if err := applyBoxSlotControl(dir, "0"); err != nil {
		t.Fatalf("set 0: %v", err)
	}
	if v, _, _ := boxslot.ReadControl(dir); v != "off" {
		t.Fatalf("control after 0 = %q, want off (0 disables)", v)
	}

	if err := applyBoxSlotControl(dir, "default"); err != nil {
		t.Fatalf("set default: %v", err)
	}
	if _, ok, _ := boxslot.ReadControl(dir); ok {
		t.Fatal("control still set after 'default'; want cleared")
	}

	if err := applyBoxSlotControl(dir, "lots"); err == nil {
		t.Fatal("expected an error for a non-numeric, non-keyword value")
	}
}

func TestRenderBoxSlotHolders_AllFormats(t *testing.T) {
	rows := []boxSlotHolderRow{
		{PID: 4242, ClaimedAt: "2026-07-01T00:00:00Z", RunID: "run-20260701-000000-cafe0001", Live: true, Lock: "/tmp/x/holder-pid4242-1-1.lock"},
		{PID: 99999, Live: false, Lock: "/tmp/x/holder-pid99999-2-1.lock"},
	}
	for _, format := range []string{"json", "plain", "pretty"} {
		if err := renderBoxSlotHolders(rows, format); err != nil {
			t.Fatalf("render %s: %v", format, err)
		}
	}
	if err := renderBoxSlotHolders(nil, "pretty"); err != nil {
		t.Fatalf("render empty pretty: %v", err)
	}
}

func TestRenderBoxSlotStalled_AllFormats(t *testing.T) {
	reapedOK, reapedNo := true, false
	rows := []boxSlotStalledRow{
		{PID: 4242, ClaimedAt: "2026-07-01T00:00:00Z", RunID: "run-20260701-000000-cafe0001",
			EnvelopeAge: "45m0s", NewestFileAge: "12s", NewestFile: "/tmp/x/runs/run-20260701-000000-cafe0001/nodes/build.log",
			Evidence: "run live but its envelope last written 45m0s ago", Lock: "/tmp/x/holder-pid4242-1-1.lock"},
		{PID: 4243, EnvelopeAge: "31m0s", Evidence: "no run annotated", Lock: "/tmp/x/holder-pid4243-2-1.lock", Reaped: &reapedOK},
		{PID: 4244, EnvelopeAge: "31m0s", Evidence: "no run annotated", Lock: "/tmp/x/holder-pid4244-3-1.lock",
			Reaped: &reapedNo, ReapError: "refusing to signal"},
	}
	for _, format := range []string{"json", "plain", "pretty"} {
		for _, reap := range []bool{false, true} {
			if err := renderBoxSlotStalled(rows, format, reap); err != nil {
				t.Fatalf("render %s reap=%t: %v", format, reap, err)
			}
		}
	}
	if err := renderBoxSlotStalled(nil, "pretty", false); err != nil {
		t.Fatalf("render empty pretty: %v", err)
	}
}

func TestLogReapAttempt_EmitsOneCountableLinePerAttempt(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	s := boxslot.StalledHolder{Holder: boxslot.Holder{
		PID: 4242, RunID: "run-20260701-000000-cafe0001", Path: "/tmp/x/holder-pid4242-1-1.lock",
	}}

	logReapAttempt(logger, s, nil)
	logReapAttempt(logger, s, fmt.Errorf("wrap: %w", boxslot.ErrHolderReleased))
	logReapAttempt(logger, s, errors.New("SIGTERM pid 4242: operation not permitted"))

	got := buf.String()
	wants := []string{
		`msg="box-slot reap attempt"`,
		"pid=4242",
		"run=run-20260701-000000-cafe0001",
		"lock=/tmp/x/holder-pid4242-1-1.lock",
		"outcome=reaped",
		"outcome=refused-released",
		"outcome=error",
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("reap telemetry %q missing %q", got, want)
		}
	}
	if n := strings.Count(got, "box-slot reap attempt"); n != 3 {
		t.Errorf("emitted %d reap lines, want one per attempt (3)", n)
	}
}

func TestReapOutcome_ClassifiesByErrorKey(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil is reaped", nil, "reaped"},
		{"released flock refusal", fmt.Errorf("w: %w", boxslot.ErrHolderReleased), "refused-released"},
		{"live holder refusal", fmt.Errorf("w: %w", boxslot.ErrHolderLive), "refused-live"},
		{"anything else is error", errors.New("find pid 4242: no such process"), "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reapOutcome(tc.err); got != tc.want {
				t.Errorf("reapOutcome = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRenderBoxSlotRelease_AllFormats(t *testing.T) {
	r := boxSlotReleaseReport{Released: "holder-pid4242-1-1.lock", Forced: true}
	for _, format := range []string{"json", "plain", "pretty"} {
		if err := renderBoxSlotRelease(r, format); err != nil {
			t.Fatalf("render %s: %v", format, err)
		}
	}
}

func TestBoxSlotHolderRowHelpers(t *testing.T) {
	if got := orDash(""); got != "-" {
		t.Errorf("orDash(\"\") = %q, want -", got)
	}
	if got := orDash("x"); got != "x" {
		t.Errorf("orDash(x) = %q, want x", got)
	}
	if got := liveWord(true); got != "live" {
		t.Errorf("liveWord(true) = %q, want live", got)
	}
	if got := liveWord(false); got != "stale" {
		t.Errorf("liveWord(false) = %q, want stale", got)
	}
}

func TestRenderBoxSlotReport_Disabled(t *testing.T) {
	r := boxSlotReport{Cap: 0, Disabled: true, Source: "control"}
	if err := renderBoxSlotReport(r, "json"); err != nil {
		t.Fatalf("render json: %v", err)
	}
	if err := renderBoxSlotReport(r, "plain"); err != nil {
		t.Fatalf("render plain: %v", err)
	}
	if err := renderBoxSlotReport(r, "pretty"); err != nil {
		t.Fatalf("render pretty: %v", err)
	}
}
