package cluster

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestObserveClaimOutcome verifies the claim counter exposes the
// three outcome values we care about (claimed, empty, error) on
// /metrics after each is called.
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

// TestObserveNodeExecution verifies the histogram emits a count row
// with the pipeline + outcome labels after a single observation.
func TestObserveNodeExecution(t *testing.T) {
	observeNodeExecution("prom-exec-pipeline", "Success", 2*time.Second)

	body := gatherMetrics(t)
	want := `sparkwing_node_execution_seconds_count{outcome="Success",pipeline="prom-exec-pipeline"} 1`
	if !strings.Contains(body, want) {
		t.Errorf("missing histogram count row %q in /metrics:\n%s", want, body)
	}
}

// TestObserveNodeExecution_SkipsEmpty guards against accidental
// unlabelled observations (empty pipeline or outcome would produce a
// high-cardinality "" label row which the cardinality guard below
// rejects).
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

// TestStartMetricsListener_ServesMetrics binds the listener on an
// ephemeral port, hits /metrics, asserts 200 + sparkwing_ prefix, and
// relies on ctx-cancel to shut it down.
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

// TestStartMetricsListener_EmptyAddrNoOps guards the short-circuit
// branch: empty --metrics-addr should return nil immediately with no
// listener bound.
func TestStartMetricsListener_EmptyAddrNoOps(t *testing.T) {
	err := StartMetricsListener(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("empty addr returned err: %v", err)
	}
}

// gatherMetrics opens an ephemeral /metrics listener against the
// package registry, reads the body, and returns the text. Simpler
// than round-tripping through a full httptest.Server.
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
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("listener did not come up at %s within %s", addr, timeout)
}
