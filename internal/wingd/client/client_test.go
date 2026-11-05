package client

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestAdmissionError_FillsVersionSkewOnOpaqueInvalid(t *testing.T) {
	cl := &Client{opts: Options{Version: "(devel)"}, ack: wingwire.HelloAck{BinaryVersion: "v0.18.0"}}
	e := cl.admissionError(&wingwire.Evicted{Key: "invalid", Policy: wingwire.PolicyFail})
	if e.Reason == "" {
		t.Fatal("opaque invalid rejection under version skew left no reason")
	}
	if !strings.Contains(e.Reason, "v0.18.0") || !strings.Contains(e.Reason, "(devel)") {
		t.Fatalf("skew reason %q does not name both versions", e.Reason)
	}
}

func TestAdmissionError_KeepsDaemonReasonWhenGiven(t *testing.T) {
	cl := &Client{opts: Options{Version: "(devel)"}, ack: wingwire.HelloAck{BinaryVersion: "v0.18.0"}}
	e := cl.admissionError(&wingwire.Evicted{Key: "invalid", Policy: wingwire.PolicyFail, Reason: "admission request invalid: named cause"})
	if e.Reason != "admission request invalid: named cause" {
		t.Fatalf("daemon-provided reason was overwritten: %q", e.Reason)
	}
}

func TestAdmissionError_NoSkewHintWhenVersionsMatch(t *testing.T) {
	cl := &Client{opts: Options{Version: "v0.18.0"}, ack: wingwire.HelloAck{BinaryVersion: "v0.18.0"}}
	e := cl.admissionError(&wingwire.Evicted{Key: "invalid", Policy: wingwire.PolicyFail})
	if e.Reason != "" {
		t.Fatalf("matched versions produced a skew hint: %q", e.Reason)
	}
}

func shortHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "wdcl")
	if err != nil {
		return t.TempDir()
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func spawnInProcess(t *testing.T, home string) func(string, string) error {
	return func(string, string) error {
		d, err := wingd.New(wingd.Config{Home: home, Version: "v1.0.0"})
		if err != nil {
			return err
		}
		ctx, cancel := context.WithCancel(context.Background())
		exited := make(chan error, 1)
		go func() { exited <- d.Run(ctx) }()
		select {
		case <-d.Ready():
			t.Cleanup(func() {
				cancel()
				select {
				case <-exited:
				case <-time.After(5 * time.Second):
					t.Error("spawned daemon did not stop after cancellation")
				}
			})
			return nil
		case err := <-exited:
			cancel()
			if errors.Is(err, wingd.ErrNotElected) {
				return nil
			}
			return err
		}
	}
}

func TestEnsureDaemon_SpawnsWhenAbsent(t *testing.T) {
	home := shortHome(t)
	cl, err := EnsureDaemon(context.Background(), Options{
		Home:        home,
		Version:     "v1.0.0",
		Spawn:       spawnInProcess(t, home),
		DialTimeout: 500 * time.Millisecond,
		Backoff:     20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ensure daemon: %v", err)
	}
	defer cl.Close()
	if cl.DaemonVersion() != "v1.0.0" {
		t.Fatalf("daemon version %q, want v1.0.0", cl.DaemonVersion())
	}
	lease, err := cl.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID:     "r1",
		Resources: wingwire.HostResources{Cores: 0.5},
	}, nil)
	if err != nil {
		t.Fatalf("acquire against spawned daemon: %v", err)
	}
	if lease.RunID != "r1" {
		t.Fatalf("lease run id %q, want r1", lease.RunID)
	}
}

func TestEnsureDaemon_BoundsAnUnreachablePredecessorElection(t *testing.T) {
	home := shortHome(t)
	_ = runDaemon(t, home, "v1.0.0")
	sock, err := wingd.EnsureSocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(wingd.APISocketBeside(sock)); err != nil {
		t.Fatalf("hide predecessor listener: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = EnsureDaemon(ctx, Options{
		Home:                   home,
		Spawn:                  func(string, string) error { t.Fatal("spawned while predecessor held election"); return nil },
		DialTimeout:            time.Millisecond,
		Backoff:                time.Nanosecond,
		PredecessorWaitTimeout: 20 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("unreachable predecessor election waited forever")
	}
	if !errors.Is(err, errDaemonUnreachable) || !strings.Contains(err.Error(), "predecessor") || !strings.Contains(err.Error(), "election lock") {
		t.Fatalf("unreachable predecessor error = %v; want bounded election-lock diagnostic", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("predecessor wait consumed the caller deadline: %v", err)
	}
}

func TestEnsureDaemon_WaitsForOneSlowHealthySpawn(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	home := shortHome(t)
	var calls atomic.Int32
	spawnRequested := make(chan struct{})
	postSpawnRetries := make(chan struct{})
	startDaemon := make(chan struct{})
	var spawnOnce, startOnce sync.Once
	var retryCount atomic.Int32
	releaseStart := func() { startOnce.Do(func() { close(startDaemon) }) }
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)

	var daemonErr error
	daemonFinished := make(chan struct{})
	go func() {
		defer close(daemonFinished)
		select {
		case <-spawnRequested:
		case <-ctx.Done():
			daemonErr = ctx.Err()
			return
		}
		select {
		case <-startDaemon:
		case <-ctx.Done():
			daemonErr = ctx.Err()
			return
		}
		d, err := wingd.New(wingd.Config{Home: home, Version: "v1.0.0"})
		if err != nil {
			daemonErr = err
			return
		}
		daemonErr = d.Run(ctx)
	}()

	spawn := func(string, string) error {
		calls.Add(1)
		spawnOnce.Do(func() { close(spawnRequested) })
		return nil
	}
	type ensureResult struct {
		cl  *Client
		err error
	}
	var result ensureResult
	ensureFinished := make(chan struct{})
	go func() {
		defer close(ensureFinished)
		result.cl, result.err = EnsureDaemon(ctx, Options{
			Home:        home,
			Version:     "v1.0.0",
			Spawn:       spawn,
			DialTimeout: 10 * time.Millisecond,
			Backoff:     10 * time.Millisecond,
			observeDialFailure: func() {
				if calls.Load() > 0 && retryCount.Add(1) == 3 {
					close(postSpawnRetries)
				}
			},
		})
	}()
	t.Cleanup(func() {
		releaseStart()
		cancel()
		joinDeadline := time.NewTimer(time.Second)
		ensureJoined := false
		select {
		case <-ensureFinished:
			ensureJoined = true
		case <-joinDeadline.C:
			t.Error("EnsureDaemon did not stop after cancellation")
		}
		joinDeadline.Stop()
		if ensureJoined && result.cl != nil {
			_ = result.cl.Close()
		}
		joinDeadline = time.NewTimer(time.Second)
		defer joinDeadline.Stop()
		select {
		case <-daemonFinished:
			if daemonErr != nil && !errors.Is(daemonErr, context.Canceled) {
				t.Errorf("spawned daemon: %v", daemonErr)
			}
		case <-joinDeadline.C:
			t.Error("spawned daemon did not stop after cancellation")
		}
	})

	select {
	case <-spawnRequested:
	case <-ctx.Done():
		t.Fatalf("spawn was not requested: %v", ctx.Err())
	}
	select {
	case <-postSpawnRetries:
	case <-ctx.Done():
		t.Fatalf("post-spawn retries were not observed: %v", ctx.Err())
	}
	releaseStart()
	select {
	case <-ensureFinished:
	case <-ctx.Done():
		t.Fatalf("ensure slow-starting daemon: %v", ctx.Err())
	}
	if result.err != nil {
		t.Fatalf("ensure slow-starting daemon: %v", result.err)
	}
	if result.cl == nil {
		t.Fatal("ensure slow-starting daemon returned no client")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("spawn calls = %d, want one startup attempt", got)
	}
}

func TestEnsureDaemon_DevClientAcceptsSameSourceReleaseDaemon(t *testing.T) {
	home := shortHome(t)
	spawn := spawnInProcess(t, home)

	connect := func(id string) (string, error) {
		cl, err := EnsureDaemon(context.Background(), Options{
			Home:        home,
			Version:     "(devel)",
			Spawn:       spawn,
			DialTimeout: 500 * time.Millisecond,
			Backoff:     20 * time.Millisecond,
		})
		if err != nil {
			return "", err
		}
		defer cl.Close()
		return cl.DaemonVersion(), nil
	}

	type result struct {
		ver string
		err error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	for i, id := range []string{"dev-run-a", "dev-run-b"} {
		wg.Add(1)
		go func(idx int, runID string) {
			defer wg.Done()
			ver, err := connect(runID)
			results[idx] = result{ver, err}
		}(i, id)
	}
	wg.Wait()
	for i, r := range results {
		if r.err != nil {
			t.Errorf("client %d: error = %v, want success", i, r.err)
		}
	}
}

func TestQuery_NoDaemonReturnsSentinel(t *testing.T) {
	home := shortHome(t)
	_, err := Query(context.Background(), Options{
		Home:        home,
		DialTimeout: 200 * time.Millisecond,
		Backoff:     10 * time.Millisecond,
	})
	if !errors.Is(err, ErrNoDaemon) {
		t.Fatalf("Query with no daemon: got %v, want ErrNoDaemon", err)
	}
}

func TestQueryOptionsNeverTakeOverOrSpawn(t *testing.T) {
	opts := queryOptions(Options{Version: "v2.0.0"})
	if !opts.NoTakeover {
		t.Fatal("read-only query remained takeover-capable")
	}
	if err := opts.Spawn("ignored", "ignored"); !errors.Is(err, ErrNoDaemon) {
		t.Fatalf("query spawn error = %v, want ErrNoDaemon", err)
	}
}
