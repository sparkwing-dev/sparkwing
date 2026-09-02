package store

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestArgon2Concurrency(t *testing.T) {
	cases := []struct {
		name   string
		budget int64
		want   int
	}{
		{name: "default budget fits four hashes", budget: DefaultArgon2MemoryBudget, want: 4},
		{name: "one hash", budget: Argon2HashBytes, want: 1},
		{name: "rounds down", budget: Argon2HashBytes*3 + 1, want: 3},
		{name: "never below one", budget: 1, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Argon2Concurrency(tc.budget); got != tc.want {
				t.Fatalf("Argon2Concurrency(%d) = %d, want %d", tc.budget, got, tc.want)
			}
		})
	}
}

func TestArgon2SemaphoreBoundsConcurrentHashes(t *testing.T) {
	restore := argonIDFunc
	prevWait := SetArgon2AcquireTimeout(time.Minute)
	t.Cleanup(func() {
		argonIDFunc = restore
		SetArgon2AcquireTimeout(prevWait)
		SetArgon2MemoryBudget(DefaultArgon2MemoryBudget)
	})

	var inFlight, peak atomic.Int64
	argonIDFunc = func(_, _ []byte, _, _ uint32, _ uint8, keyLen uint32) []byte {
		n := inFlight.Add(1)
		for {
			seen := peak.Load()
			if n <= seen || peak.CompareAndSwap(seen, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		return make([]byte, keyLen)
	}

	const want = 2
	if got := SetArgon2MemoryBudget(want * Argon2HashBytes); got != want {
		t.Fatalf("SetArgon2MemoryBudget returned %d, want %d", got, want)
	}

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := hashPassword("correct horse battery staple"); err != nil {
				t.Errorf("hashPassword: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := peak.Load(); got > want {
		t.Fatalf("peak concurrent hashes = %d, want at most %d", got, want)
	}
	if got := peak.Load(); got < want {
		t.Fatalf("peak concurrent hashes = %d, want the full budget used", got)
	}
}

func TestSetArgon2MemoryBudgetIgnoresNonPositive(t *testing.T) {
	t.Cleanup(func() { SetArgon2MemoryBudget(DefaultArgon2MemoryBudget) })
	SetArgon2MemoryBudget(3 * Argon2HashBytes)
	if got := SetArgon2MemoryBudget(0); got != 3 {
		t.Fatalf("zero budget changed concurrency to %d, want 3", got)
	}
	if got := SetArgon2MemoryBudget(-1); got != 3 {
		t.Fatalf("negative budget changed concurrency to %d, want 3", got)
	}
}

func TestArgon2SaturationShedsInsteadOfQueueing(t *testing.T) {
	restore := argonIDFunc
	prevWait := SetArgon2AcquireTimeout(20 * time.Millisecond)
	t.Cleanup(func() {
		argonIDFunc = restore
		SetArgon2AcquireTimeout(prevWait)
		SetArgon2MemoryBudget(DefaultArgon2MemoryBudget)
	})

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	argonIDFunc = func(_, _ []byte, _, _ uint32, _ uint8, keyLen uint32) []byte {
		entered <- struct{}{}
		<-release
		return make([]byte, keyLen)
	}
	if got := SetArgon2MemoryBudget(Argon2HashBytes); got != 1 {
		t.Fatalf("SetArgon2MemoryBudget returned %d, want 1", got)
	}

	held := make(chan struct{})
	go func() {
		defer close(held)
		_, _ = hashPassword("holder")
	}()
	<-entered
	defer func() {
		close(release)
		<-held
	}()

	start := time.Now()
	_, err := hashPassword("shed me")
	elapsed := time.Since(start)
	if !errors.Is(err, ErrHashingBusy) {
		t.Fatalf("saturated hash returned %v, want ErrHashingBusy", err)
	}
	if elapsed > time.Second {
		t.Fatalf("saturated hash waited %v, want it shed near the acquire timeout", elapsed)
	}
}

func TestVerifyUserShedsWhenHashingIsSaturated(t *testing.T) {
	restore := argonIDFunc
	prevWait := SetArgon2AcquireTimeout(20 * time.Millisecond)
	t.Cleanup(func() {
		argonIDFunc = restore
		SetArgon2AcquireTimeout(prevWait)
		SetArgon2MemoryBudget(DefaultArgon2MemoryBudget)
	})

	st := newTestStore(t)
	now := time.Now().UTC()
	if _, err := st.CreateUser("alice", "correct horse battery", []string{"admin"}, now); err != nil {
		t.Fatalf("create alice: %v", err)
	}

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	argonIDFunc = func(_, _ []byte, _, _ uint32, _ uint8, keyLen uint32) []byte {
		entered <- struct{}{}
		<-release
		return make([]byte, keyLen)
	}
	SetArgon2MemoryBudget(Argon2HashBytes)

	held := make(chan struct{})
	go func() {
		defer close(held)
		_, _ = st.VerifyUser("alice", "correct horse battery", now)
	}()
	<-entered
	defer func() {
		close(release)
		<-held
	}()

	for _, name := range []string{"alice", "nobody"} {
		if _, err := st.VerifyUser(name, "whatever", now); !errors.Is(err, ErrHashingBusy) {
			t.Fatalf("VerifyUser(%q) under saturation returned %v, want ErrHashingBusy", name, err)
		}
	}
}
