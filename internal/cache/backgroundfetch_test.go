package cache

import (
	"context"
	"testing"
	"time"
)

// safety: the loop reads the path globals, so it has to be joined before a cleanup restores them.
func startBackgroundFetch(t *testing.T, interval time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		backgroundFetchLoop(ctx, interval)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func TestBackgroundFetchWaitsForTheHandlerLockOnTheSameRepo(t *testing.T) {
	repoURL, _, _ := gitcacheFixture(t)

	started := make(chan struct{}, 1)
	old := mirrorFetch
	mirrorFetch = func(time.Duration, string) (string, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		return "", nil
	}
	t.Cleanup(func() { mirrorFetch = old })

	lock := repoLock(repoHash(repoURL))
	lock.Lock()

	startBackgroundFetch(t, 5*time.Millisecond)

	select {
	case <-started:
		lock.Unlock()
		t.Fatal("background fetch ran while a request handler held the repo lock")
	case <-time.After(250 * time.Millisecond):
	}

	lock.Unlock()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("background fetch never ran after the repo lock was released")
	}
}
