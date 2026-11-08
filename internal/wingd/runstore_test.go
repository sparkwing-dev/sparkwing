package wingd_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestSlowRunStoreDoesNotBlockOtherConnections(t *testing.T) {
	home := shortHome(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	startDaemon(t, wingd.Config{
		Home: home,
		Runs: &wingd.FuncRunStore{IsTerminal: func(string) (bool, error) {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return false, nil
		}},
	})

	blocked := ensure(t, home, "")
	_, admitted := acquireAsync(t, blocked, coreReq("slow-store", 1))
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("admission never reached the store")
	}

	other, err := client.EnsureDaemon(ctx, client.Options{Home: home, Version: "v1.0.0", Spawn: errSpawn, DialTimeout: time.Second, Backoff: 10 * time.Millisecond})
	if err != nil {
		unblock()
		t.Fatalf("probe while the store is slow: %v", err)
	}
	defer other.Close()
	if _, err := other.QueueState(ctx); err != nil {
		t.Fatalf("queue state while the store is slow: %v", err)
	}

	unblock()
	result := waitResult(t, admitted, 2*time.Second)
	if result.err != nil {
		t.Fatalf("acquire: %v", result.err)
	}
	if err := result.lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestProbeReportsTheDaemonStoreIsReady(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Runs: &wingd.FuncRunStore{}})

	sock, err := wingd.EnsureSocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	info, err := client.Probe(context.Background(), sock)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if info.StoreReady == nil {
		t.Fatal("a daemon holding a store omitted its readiness")
	}
	if !*info.StoreReady || info.StoreError != "" {
		t.Fatalf("store readiness = (%v, %q), want ready", *info.StoreReady, info.StoreError)
	}
}

func TestProbeReportsWhyTheStoreIsUnusable(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home: home,
		Runs: &wingd.FuncRunStore{NotReady: errors.New("database is at schema version 26; this binary expects 17")},
	})

	sock, err := wingd.EnsureSocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	info, err := client.Probe(context.Background(), sock)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if info.StoreReady == nil {
		t.Fatal("a daemon with an unusable store omitted its readiness instead of answering false")
	}
	if *info.StoreReady {
		t.Fatal("an unusable store reported ready")
	}
	if info.StoreError != "database is at schema version 26; this binary expects 17" {
		t.Fatalf("store error = %q, want the store failure verbatim", info.StoreError)
	}
}
