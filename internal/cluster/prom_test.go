package cluster

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestObserveClaimOutcome(t *testing.T) {
	observeClaimOutcome("claimed")
	observeClaimOutcome("empty")
	observeClaimOutcome("error")

	body := gatherMetrics(t)
	for _, want := range []string{
		`sparkwing_runner_claims_total{outcome="claimed"}`,
		`sparkwing_runner_claims_total{outcome="empty"}`,
		`sparkwing_runner_claims_total{outcome="error"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in /metrics output:\n%s", want, body)
		}
	}
}

func TestObserveNodeExecution(t *testing.T) {
	const prefix = `sparkwing_node_execution_seconds_count{outcome="Success",pipeline="prom-exec-pipeline"}`
	before := metricSampleValue(t, gatherMetrics(t), prefix)
	observeNodeExecution("prom-exec-pipeline", "Success", 2*time.Second)

	if got := metricSampleValue(t, gatherMetrics(t), prefix); got != before+1 {
		t.Errorf("node execution count=%v before=%v, want one observation", got, before)
	}
}

func metricSampleValue(t *testing.T, body, prefix string) float64 {
	t.Helper()
	for line := range strings.SplitSeq(body, "\n") {
		value, ok := strings.CutPrefix(line, prefix+" ")
		if !ok {
			continue
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			t.Fatalf("parse metric sample %q: %v", line, err)
		}
		return parsed
	}
	return 0
}

func TestObserveNodeExecution_SkipsEmpty(t *testing.T) {
	observeNodeExecution("", "Success", 1*time.Second)
	observeNodeExecution("some-pipeline", "", 1*time.Second)
	observeNodeExecution("some-pipeline", "Success", 0)

	body := gatherMetrics(t)
	if strings.Contains(body, `outcome="",pipeline=`) ||
		strings.Contains(body, `pipeline="",outcome=`) {
		t.Errorf("observeNodeExecution emitted empty label row:\n%s", body)
	}
}

func TestStartMetricsListener_ServesMetrics(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- StartMetricsListener(ctx, addr, nil) }()

	waitForListener(t, addr, 2*time.Second)

	resp, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "sparkwing_") {
		t.Errorf("/metrics output missing sparkwing_ prefix:\n%s", string(body))
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("listener returned err: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("listener did not shut down on ctx cancel")
	}
}

func TestStartMetricsListener_EmptyAddrNoOps(t *testing.T) {
	err := StartMetricsListener(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("empty addr returned err: %v", err)
	}
}

func gatherMetrics(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = StartMetricsListener(ctx, addr, nil) }()
	waitForListener(t, addr, 2*time.Second)

	resp, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func waitForListener(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	dialer := net.Dialer{Timeout: 100 * time.Millisecond}
	for {
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			conn.Close()
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("listener did not come up at %s within %s", addr, timeout)
		}
	}
}
