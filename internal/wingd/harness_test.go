package wingd_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// fakeSampler feeds a controllable host reading so admission gating is
// exercised without touching the real machine.
type fakeSampler struct {
	mu   sync.Mutex
	stat wingd.HostStat
}

func newFakeSampler(cores float64, mem uint64) *fakeSampler {
	return &fakeSampler{stat: wingd.HostStat{
		TotalCores:       cores,
		TotalMemoryBytes: mem,
		FreeMemoryBytes:  mem,
	}}
}

func (f *fakeSampler) Sample() (wingd.HostStat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stat, nil
}

func (f *fakeSampler) set(stat wingd.HostStat) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stat = stat
}

// shortHome returns a scratch sparkwing home under /tmp so the unix
// socket path stays within the OS length limit.
func shortHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "wd")
	if err != nil {
		dir = t.TempDir()
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	}
	return dir
}

type testDaemon struct {
	d    *wingd.Daemon
	done chan error
	stop context.CancelFunc
}

// startDaemon runs a daemon in the background and waits until it is
// serving. It fails the test if the daemon exits before becoming ready.
func startDaemon(t *testing.T, cfg wingd.Config) *testDaemon {
	t.Helper()
	if cfg.Sampler == nil {
		cfg.Sampler = newFakeSampler(64, 64<<30)
	}
	d, err := wingd.New(cfg)
	if err != nil {
		t.Fatalf("new daemon: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	td := &testDaemon{d: d, done: make(chan error, 1), stop: cancel}
	go func() { td.done <- d.Run(ctx) }()
	select {
	case <-d.Ready():
	case err := <-td.done:
		cancel()
		t.Fatalf("daemon exited before ready: %v", err)
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("daemon never became ready")
	}
	t.Cleanup(cancel)
	return td
}

func (td *testDaemon) waitExit(t *testing.T, within time.Duration) error {
	t.Helper()
	select {
	case err := <-td.done:
		return err
	case <-time.After(within):
		t.Fatalf("daemon did not exit within %s", within)
		return nil
	}
}

func errSpawn(string, string) error {
	return errors.New("spawn not expected: daemon already running")
}

// ensure connects a client to an already-running daemon; Spawn must not
// fire because the daemon is up.
func ensure(t *testing.T, home, version string) *client.Client {
	t.Helper()
	cl, err := client.EnsureDaemon(context.Background(), client.Options{
		Home:        home,
		Version:     version,
		Spawn:       errSpawn,
		DialTimeout: time.Second,
		Backoff:     10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ensure daemon: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}

// semReq builds an admission request holding one named semaphore.
func semReq(runID, key string, capacity, cost int, policy wingwire.Policy) wingwire.AdmissionRequest {
	return wingwire.AdmissionRequest{
		RunID:     runID,
		Resources: wingwire.HostResources{Cores: 0.1},
		Semaphores: []wingwire.SemaphoreClaim{
			{Name: key, Capacity: capacity, Cost: cost, Policy: policy},
		},
	}
}

type acquireResult struct {
	lease *client.Lease
	err   error
}

// acquireAsync starts an Acquire in the background, reporting queue
// positions on the returned channel and the final outcome on the result
// channel.
func acquireAsync(cl *client.Client, req wingwire.AdmissionRequest) (<-chan wingwire.Queued, <-chan acquireResult) {
	positions := make(chan wingwire.Queued, 8)
	result := make(chan acquireResult, 1)
	go func() {
		lease, err := cl.Acquire(context.Background(), req, func(q wingwire.Queued) {
			select {
			case positions <- q:
			default:
			}
		})
		result <- acquireResult{lease: lease, err: err}
	}()
	return positions, result
}

func mustAcquire(t *testing.T, cl *client.Client, req wingwire.AdmissionRequest) *client.Lease {
	t.Helper()
	lease, err := cl.Acquire(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("acquire %s: %v", req.RunID, err)
	}
	return lease
}

func waitResult(t *testing.T, ch <-chan acquireResult, within time.Duration) acquireResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(within):
		t.Fatal("acquire did not resolve in time")
		return acquireResult{}
	}
}
