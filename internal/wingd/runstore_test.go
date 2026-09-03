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
	if !info.StoreReady || info.StoreError != "" {
		t.Fatalf("store readiness = (%v, %q), want ready", info.StoreReady, info.StoreError)
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
	if info.StoreReady {
		t.Fatal("an unusable store reported ready")
	}
	if info.StoreError != "database is at schema version 26; this binary expects 17" {
		t.Fatalf("store error = %q, want the store failure verbatim", info.StoreError)
	}
}
