package main

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestFmtDaemonHeader(t *testing.T) {
	if got := fmtDaemonHeader(wingwire.QueueState{}); got != "" {
		t.Errorf("empty state header = %q, want empty", got)
	}
	got := fmtDaemonHeader(wingwire.QueueState{DaemonVersion: "v0.16.0", DaemonUptimeMS: 125_000})
	if !strings.Contains(got, "v0.16.0") || !strings.Contains(got, "up 2m5s") {
		t.Errorf("header = %q, want daemon version and uptime", got)
	}
	if got := fmtDaemonHeader(wingwire.QueueState{DaemonVersion: "v0.16.0"}); !strings.Contains(got, "just started") {
		t.Errorf("zero-uptime header = %q, want 'just started'", got)
	}
}

func TestRenderQueuePretty_ShowsDaemonHeader(t *testing.T) {
	var b strings.Builder
	qs := wingwire.QueueState{DaemonVersion: "v0.16.0", DaemonUptimeMS: 60_000}
	if err := renderQueuePretty(&b, qs); err != nil {
		t.Fatalf("renderQueuePretty: %v", err)
	}
	if !strings.Contains(b.String(), "daemon v0.16.0") {
		t.Errorf("pretty queue missing daemon header:\n%s", b.String())
	}
}
