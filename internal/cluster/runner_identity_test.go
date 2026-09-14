package cluster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type identityCapture struct {
	mu   sync.Mutex
	seen map[string][]string
}

func newIdentityCapture() (*identityCapture, *httptest.Server) {
	c := &identityCapture{seen: map[string][]string{}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.seen[r.URL.Path] = append(c.seen[r.URL.Path], r.Header.Get(store.RunnerIdentityHeader))
		c.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	return c, ts
}

func (c *identityCapture) first(path string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.seen[path]) == 0 {
		return "", false
	}
	return c.seen[path][0], true
}

func (c *identityCapture) stable(path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, got := range c.seen[path] {
		if got != c.seen[path][0] {
			return false
		}
	}
	return len(c.seen[path]) > 1
}

// The budget only bites when the identity comes from the loop's own
// constructor and holds still across polls, so these assertions run the
// shipped entry points rather than a client built for the test.
func TestRunPoolLoop_NamesItsRunnerOnEveryClaim(t *testing.T) {
	capture, ts := newIdentityCapture()
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err := RunPoolLoop(ctx, PoolLoopConfig{
		ControllerURL: ts.URL,
		HolderPrefix:  "runner:pool-host",
		MaxConcurrent: 1,
		PollInterval:  time.Millisecond,
		SourceName:    "test runner",
	}, discardLogger())
	if err != nil {
		t.Fatalf("RunPoolLoop: %v", err)
	}

	got, ok := capture.first("/api/v1/nodes/claim")
	if !ok {
		t.Fatal("the pool loop never claimed")
	}
	want := "runner:pool-host:" + strconv.Itoa(os.Getpid())
	if got != want {
		t.Errorf("claim sent %s=%q, want %q: two runner processes on one host share a "+
			"holder prefix and only the process id tells them apart",
			store.RunnerIdentityHeader, got, want)
	}
	if !capture.stable("/api/v1/nodes/claim") {
		t.Error("the identity changed between polls; each poll would buy a fresh budget")
	}
}

func TestRunTriggerLoop_NamesItsRunnerOnEveryClaim(t *testing.T) {
	capture, ts := newIdentityCapture()
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err := RunTriggerLoop(ctx, TriggerLoopOptions{
		ControllerURL: ts.URL,
		GitcacheURL:   ts.URL,
		WorkRoot:      t.TempDir(),
		Poll:          time.Millisecond,
		MaxConcurrent: 1,
		Logger:        discardLogger(),
	})
	if err != nil {
		t.Fatalf("RunTriggerLoop: %v", err)
	}

	got, ok := capture.first("/api/v1/triggers/claim")
	if !ok {
		t.Fatal("the trigger loop never claimed")
	}
	if !strings.HasPrefix(got, "trigger-loop:") {
		t.Errorf("claim sent %s=%q, want an identity naming this trigger-loop process",
			store.RunnerIdentityHeader, got)
	}
	if !capture.stable("/api/v1/triggers/claim") {
		t.Error("the identity changed between polls; each poll would buy a fresh budget")
	}
}
