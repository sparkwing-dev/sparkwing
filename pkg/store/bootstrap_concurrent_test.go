package store_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestCreateFirstUser_ConcurrentBootstrapAdmitsOneAdmin(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(t *testing.T) *store.Store
	}{
		{name: "sqlite", open: newStoreT},
		{name: "postgres", open: openPGTestStore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raceBootstrap(t, tc.open(t), 8)
		})
	}
}

func raceBootstrap(t *testing.T, s *store.Store, racers int) {
	t.Helper()

	errs := make([]error, racers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = s.CreateFirstUser(
				fmt.Sprintf("admin%d", i), "correct-horse-battery",
				[]string{"admin"}, time.Now(),
			)
		}(i)
	}
	close(start)
	wg.Wait()

	created, closed := 0, 0
	for i, err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, store.ErrBootstrapClosed):
			closed++
		case errors.Is(err, store.ErrHashingBusy):
		default:
			t.Fatalf("racer %d: want nil, ErrBootstrapClosed or ErrHashingBusy, got %v", i, err)
		}
	}
	if created+closed < 2 {
		t.Fatalf("only %d racers reached the store, so nothing raced", created+closed)
	}
	if created != 1 {
		t.Fatalf("bootstrap admitted %d admins, want exactly 1", created)
	}

	n, err := s.CountUsers()
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if n != 1 {
		t.Fatalf("users table holds %d rows after a bootstrap race, want 1", n)
	}
}
