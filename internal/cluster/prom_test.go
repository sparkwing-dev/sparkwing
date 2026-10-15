package cluster

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestMetricsCountUnifiedClaimsWithoutChangingResults(t *testing.T) {
	claim := &controllerwire.ClaimedUnit{UnitID: "award"}
	failure := errors.New("claim failed")
	for _, item := range []struct {
		outcome string
		claim   *controllerwire.ClaimedUnit
		err     error
	}{{"claimed", claim, nil}, {"empty", nil, nil}, {"error", nil, failure}, {"error", claim, failure}} {
		prefix := `sparkwing_runner_claims_total{outcome="` + item.outcome + `"}`
		before := metricSampleValue(t, gatherMetrics(t), prefix)
		controller := metricsController{cliClaim(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			return controllerwire.ClaimResponse{Claim: item.claim}, item.err
		})}
		response, err := controller.Claim(t.Context(), controllerwire.ClaimRequest{})
		if response.Claim != item.claim || !errors.Is(err, item.err) {
			t.Fatalf("changed award or error: %+v %v", response, err)
		}
		if got := metricSampleValue(t, gatherMetrics(t), prefix); got != before+1 {
			t.Fatalf("%s claims = %v, before = %v", item.outcome, got, before)
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

func TestRunnerMetricsServesAndJoins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		listener := newMetricsListener()
		done := make(chan error, 1)
		runnerJoined := false
		go func() {
			done <- runWithMetrics(ctx, listener, func(ctx context.Context) error {
				<-ctx.Done()
				runnerJoined = true
				return nil
			})
		}()
		transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
			client, server := net.Pipe()
			listener.connections <- server
			return client, nil
		}}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport}
		observeClaimOutcome("claimed")
		for _, path := range []string{"/healthz", "/metrics"} {
			response, err := client.Get("http://metrics" + path)
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			if err := errors.Join(readErr, closeErr); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK || (path == "/metrics" && !strings.Contains(string(body), "sparkwing_runner_claims_total")) {
				t.Fatalf("%s: status=%d body=%s", path, response.StatusCode, body)
			}
		}
		cancel()
		if err := <-done; err != nil || !runnerJoined {
			t.Fatalf("joined=%t error=%v", runnerJoined, err)
		}
		select {
		case <-listener.closed:
		default:
			t.Fatal("metrics listener survived runner shutdown")
		}
	})
}

func TestRunnerMetricsFailureCancelsAndJoinsRunner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		listener := newMetricsListener()
		listener.failure = errors.New("metrics listener failed")
		joined := false
		err := runWithMetrics(t.Context(), listener, func(ctx context.Context) error {
			<-ctx.Done()
			joined = true
			return nil
		})
		if !errors.Is(err, listener.failure) || !joined {
			t.Fatalf("joined=%t error=%v", joined, err)
		}
	})
}

func TestRunnerExitStopsMetrics(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		listener := newMetricsListener()
		failure := errors.New("runner failed")
		err := runWithMetrics(t.Context(), listener, func(context.Context) error { return failure })
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
		select {
		case <-listener.closed:
		default:
			t.Fatal("metrics listener survived runner exit")
		}
	})
}

type metricsListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
	failure     error
}

func newMetricsListener() *metricsListener {
	return &metricsListener{connections: make(chan net.Conn, 1), closed: make(chan struct{})}
}
func (l *metricsListener) Accept() (net.Conn, error) {
	if l.failure != nil {
		return nil, l.failure
	}
	select {
	case connection := <-l.connections:
		return connection, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *metricsListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}
func (*metricsListener) Addr() net.Addr { return &net.TCPAddr{} }

// safety: reading the registry through the listener's own handler leaves no gap
// between picking a port and binding it for another test process to take.
func gatherMetrics(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}
