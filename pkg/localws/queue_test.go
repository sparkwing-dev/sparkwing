package localws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// shortDaemonHome returns a scratch home under /tmp so the daemon's unix
// socket path stays within the OS length limit.
func shortDaemonHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "lwq")
	if err != nil {
		return t.TempDir()
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func startDaemonForQueue(t *testing.T, home string) {
	t.Helper()
	d, err := wingd.New(wingd.Config{Home: home, HeadroomFraction: -1})
	if err != nil {
		t.Fatalf("new daemon: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	select {
	case <-d.Ready():
	case err := <-done:
		cancel()
		t.Fatalf("daemon exited before ready: %v", err)
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("daemon never became ready")
	}
	t.Cleanup(cancel)
}

func TestQueueHandler_NoDaemonReturnsEmptyQueue(t *testing.T) {
	home := shortDaemonHome(t)
	rec := httptest.NewRecorder()
	queueHandler(home).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/queue", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with no daemon running", rec.Code)
	}
	var qs wingwire.QueueState
	if err := json.Unmarshal(rec.Body.Bytes(), &qs); err != nil {
		t.Fatalf("body is not valid queue JSON: %v\n%s", err, rec.Body.String())
	}
	if len(qs.Holders) != 0 || len(qs.Waiters) != 0 {
		t.Fatalf("no-daemon queue should be empty: %+v", qs)
	}
}

func TestQueueHandler_ProxiesDaemonQueueState(t *testing.T) {
	home := shortDaemonHome(t)
	startDaemonForQueue(t, home)

	ctx := context.Background()
	cl, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{
		Home:        home,
		Spawn:       func(string, string) error { return errors.New("daemon should already be running") },
		DialTimeout: time.Second,
		Backoff:     10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })

	lease, err := cl.Acquire(ctx, wingwire.AdmissionRequest{
		RunID:     "run-dash",
		Pipeline:  "deploy",
		Resources: wingwire.HostResources{Cores: 1},
	}, nil)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(func() { _ = lease.Release() })

	rec := httptest.NewRecorder()
	queueHandler(home).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/queue", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var qs wingwire.QueueState
	if err := json.Unmarshal(rec.Body.Bytes(), &qs); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if len(qs.Holders) != 1 || qs.Holders[0].RunID != "run-dash" || qs.Holders[0].Pipeline != "deploy" {
		t.Fatalf("endpoint did not proxy the live holder: %+v", qs.Holders)
	}
	if resourceHeld(qs, "cores") != 1 {
		t.Fatalf("cores held via endpoint = %v, want 1", resourceHeld(qs, "cores"))
	}
}

func resourceHeld(qs wingwire.QueueState, key string) float64 {
	for _, r := range qs.Resources {
		if r.Key == key {
			return r.Held
		}
	}
	return -1
}
