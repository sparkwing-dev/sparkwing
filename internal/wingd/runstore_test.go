package wingd_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestSlowRunStoreDoesNotBlockOtherConnections(t *testing.T) {
	home := shortHome(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
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
	admitted := make(chan error, 1)
	go func() {
		lease, err := blocked.Acquire(context.Background(), coreReq("slow-store", 1), nil)
		if lease != nil {
			_ = lease.Release()
		}
		admitted <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("admission never reached the store")
	}

	other := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := other.QueueState(ctx); err != nil {
		t.Fatalf("queue state while the store is slow: %v", err)
	}

	close(release)
	if err := <-admitted; err != nil {
		t.Fatalf("acquire: %v", err)
	}
}

func TestHandshakeReportsTheDaemonStoreIsReady(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Runs: &wingd.FuncRunStore{}})

	sock, err := wingd.SocketPath(home)
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

func TestHandshakeReportsWhyTheStoreIsUnusable(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home: home,
		Runs: &wingd.FuncRunStore{NotReady: errors.New("database is at schema version 26; this binary expects 17")},
	})

	sock, err := wingd.SocketPath(home)
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

func TestShutdownWaitsForAnInFlightFinalize(t *testing.T) {
	home := shortHome(t)
	entered := make(chan struct{})
	finalized := make(chan string, 1)
	td := startDaemon(t, wingd.Config{
		Home: home,
		Runs: &wingd.FuncRunStore{Finalize: func(runID string) {
			close(entered)
			time.Sleep(300 * time.Millisecond)
			finalized <- runID
		}},
	})

	cl := ensure(t, home, "")
	mustAcquire(t, cl, coreReq("drain-finalize", 1))
	cl.Close()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the orphaned run was never finalized")
	}

	td.stopAndWait(t)
	select {
	case got := <-finalized:
		if got != "drain-finalize" {
			t.Fatalf("finalized %q, want drain-finalize", got)
		}
	default:
		t.Fatal("shutdown returned before an in-flight finalize finished; the host closes the store next")
	}
}
