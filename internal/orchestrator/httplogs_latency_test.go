package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type modeledRTTLogTransport struct {
	appends atomic.Int64
	mu      sync.Mutex
	batches []capturedLogBatch
	latency time.Duration
}

type capturedLogBatch struct {
	start, end int64
	body       []byte
	holder     string
	attempt    string
}

func (t *modeledRTTLogTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.latency > 0 {
		ready := make(chan struct{})
		clock := time.AfterFunc(t.latency, func() { close(ready) })
		defer clock.Stop()
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-ready:
		}
	}
	if !strings.HasSuffix(r.URL.Path, "/seal") {
		t.appends.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		first, _ := strconv.ParseInt(r.Header.Get(logs.LogSeqHeader), 10, 64)
		last := first
		if raw := r.Header.Get(logs.LogSeqEndHeader); raw != "" {
			last, _ = strconv.ParseInt(raw, 10, 64)
		}
		t.mu.Lock()
		t.batches = append(t.batches, capturedLogBatch{
			start: first, end: last, body: body,
			holder:  r.Header.Get(store.ClaimHolderHeader),
			attempt: r.Header.Get(store.AttemptOrdinalHeader),
		})
		t.mu.Unlock()
	}
	return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Header: make(http.Header), Request: r}, nil
}

func TestHTTPLogs_TwelveThousandLinesFitTheWSLTransportBudget(t *testing.T) {
	synctest.Test(t, testTwelveThousandLinesFitTheWSLTransportBudget)
}

func testTwelveThousandLinesFitTheWSLTransportBudget(t *testing.T) {
	const lines = 12_000
	const rtt = 32 * time.Millisecond
	transport := &modeledRTTLogTransport{latency: rtt}
	backend := orchestrator.NewHTTPLogs("http://logs.invalid", &http.Client{Transport: transport}, nil)
	log, err := backend.OpenNodeLog(context.Background(), "run", "build", nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := range lines {
		log.Emit(sparkwing.LogRecord{Level: "info", Msg: fmt.Sprintf("small line %d", i)})
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	posts := transport.appends.Load()
	modeledNetwork := time.Duration(posts) * rtt
	t.Logf("%d lines, %d append POSTs, modeled 32ms RTT cost %s, local wall %s", lines, posts, modeledNetwork, time.Since(start))
	if modeledNetwork > 5*time.Second {
		t.Fatalf("12k lines cost %s at 32ms RTT through %d POSTs, want at most 5s", modeledNetwork, posts)
	}
	var next int64 = 1
	for _, batch := range transport.batches {
		if batch.start != next {
			t.Fatalf("batch starts at %d, want %d", batch.start, next)
		}
		rows := strings.Split(strings.TrimSuffix(string(batch.body), "\n"), "\n")
		if len(rows) > 256 || len(batch.body) > 64<<10 {
			t.Fatalf("batch has %d lines and %d bytes", len(rows), len(batch.body))
		}
		if int64(len(rows)) != batch.end-batch.start+1 {
			t.Fatalf("range %d..%d contains %d lines", batch.start, batch.end, len(rows))
		}
		for _, row := range rows {
			var rec sparkwing.LogRecord
			if err := json.Unmarshal([]byte(row), &rec); err != nil {
				t.Fatal(err)
			}
			if rec.Msg != fmt.Sprintf("small line %d", next-1) {
				t.Fatalf("sequence %d message = %q", next, rec.Msg)
			}
			next++
		}
	}
	if next != lines+1 {
		t.Fatalf("last sequence = %d, want %d", next-1, lines)
	}
}

func TestHTTPLogs_BatchesStayWithinClaimAndAttempt(t *testing.T) {
	transport := &modeledRTTLogTransport{}
	backend := orchestrator.NewHTTPLogs("http://logs.invalid", &http.Client{Transport: transport}, nil)
	open := func(holder string) orchestrator.NodeLog {
		ctx := store.WithNodeClaimFence(context.Background(), store.NodeClaimFence{HolderID: holder, ClaimGeneration: 1})
		log, err := backend.OpenNodeLog(ctx, "run", "build", nil)
		if err != nil {
			t.Fatal(err)
		}
		return log
	}
	a, b := open("A"), open("B")
	a.Emit(sparkwing.LogRecord{Msg: "A1"})
	b.Emit(sparkwing.LogRecord{Msg: "B1"})
	for _, log := range []orchestrator.NodeLog{a, b} {
		if err := log.(interface{ BindExecutionAttempt(int) error }).BindExecutionAttempt(1); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.(interface{ FlushExecutionAttempt() error }).FlushExecutionAttempt(); err != nil {
		t.Fatal(err)
	}
	if err := a.(interface{ BindExecutionAttempt(int) error }).BindExecutionAttempt(2); err != nil {
		t.Fatal(err)
	}
	a.Emit(sparkwing.LogRecord{Msg: "A2"})
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if len(transport.batches) != 3 {
		t.Fatalf("batches = %d, want three claim/attempt partitions", len(transport.batches))
	}
	for _, batch := range transport.batches {
		var rec sparkwing.LogRecord
		if err := json.Unmarshal(bytes.TrimSpace(batch.body), &rec); err != nil {
			t.Fatal(err)
		}
		want := batch.holder + batch.attempt
		if rec.Msg != want {
			t.Fatalf("claim %s attempt %s contains %q", batch.holder, batch.attempt, rec.Msg)
		}
	}
}

func TestHTTPLogs_IdleTailStartsFlushAtOneHundredMilliseconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &modeledRTTLogTransport{}
		backend := orchestrator.NewHTTPLogs("http://logs.invalid", &http.Client{Transport: transport}, nil)
		log, err := backend.OpenNodeLog(context.Background(), "run", "build", nil)
		if err != nil {
			t.Fatal(err)
		}
		log.Emit(sparkwing.LogRecord{Msg: "tail"})
		if transport.appends.Load() != 0 {
			t.Fatal("idle tail appended before the batch delay")
		}
		checkpoint := make(chan struct{})
		clock := time.AfterFunc(101*time.Millisecond, func() { close(checkpoint) })
		defer clock.Stop()
		<-checkpoint
		synctest.Wait()
		if transport.appends.Load() != 1 {
			t.Fatalf("tail append count = %d at 101 ms", transport.appends.Load())
		}
		if err := log.Close(); err != nil {
			t.Fatal(err)
		}
	})
}
