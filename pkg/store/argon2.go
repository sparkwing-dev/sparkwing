package store

import (
	"errors"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// DefaultArgon2MemoryBudget is the resident memory ceiling for
// concurrent argon2id hashing. Each hash holds argonMemory KiB while
// it runs, so the default admits four at a time.
const DefaultArgon2MemoryBudget = int64(256) << 20

// Argon2HashBytes is the memory one argon2id hash holds while it runs.
const Argon2HashBytes = int64(argonMemory) << 10

// DefaultArgon2AcquireTimeout bounds how long a hash waits for a free
// slot. Past it the caller is shed rather than queued, so a flood
// cannot grow an unbounded backlog of goroutines each pinning a
// password.
const DefaultArgon2AcquireTimeout = 250 * time.Millisecond

// ErrHashingBusy reports that every argon2id slot was taken for the
// whole acquire timeout. It is a load signal, not an authentication
// result: answer it with 503 and a Retry-After, never with 401.
var ErrHashingBusy = errors.New("hashing capacity is saturated")

var argonIDFunc = argon2.IDKey

var (
	argonSemMu   sync.RWMutex
	argonSem     = make(chan struct{}, Argon2Concurrency(DefaultArgon2MemoryBudget))
	argonSemWait = DefaultArgon2AcquireTimeout
)

// Argon2Concurrency reports how many concurrent argon2id hashes fit in
// budget bytes. It never reports less than one.
func Argon2Concurrency(budget int64) int {
	slots := budget / Argon2HashBytes
	if slots < 1 {
		return 1
	}
	return int(slots)
}

// Argon2Slots reports the concurrent argon2id hashing bound currently
// in force.
func Argon2Slots() int {
	argonSemMu.RLock()
	defer argonSemMu.RUnlock()
	return cap(argonSem)
}

// SetArgon2MemoryBudget bounds concurrent argon2id hashing to what
// fits in budget bytes and returns the resulting concurrency. Hashes
// already running are unaffected; they release against the semaphore
// they acquired. A non-positive budget leaves the bound unchanged.
func SetArgon2MemoryBudget(budget int64) int {
	if budget <= 0 {
		return Argon2Slots()
	}
	slots := Argon2Concurrency(budget)
	argonSemMu.Lock()
	defer argonSemMu.Unlock()
	argonSem = make(chan struct{}, slots)
	return slots
}

// SetArgon2AcquireTimeout bounds the wait for a free hashing slot and
// returns the previous bound. A non-positive duration leaves it
// unchanged.
func SetArgon2AcquireTimeout(d time.Duration) time.Duration {
	argonSemMu.Lock()
	defer argonSemMu.Unlock()
	prev := argonSemWait
	if d > 0 {
		argonSemWait = d
	}
	return prev
}

func argonKey(secret string, salt []byte) ([]byte, error) {
	argonSemMu.RLock()
	sem, wait := argonSem, argonSemWait
	argonSemMu.RUnlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	// safety: an unauthenticated caller drives these hashes, so a full semaphore sheds the request instead of queueing it.
	select {
	case sem <- struct{}{}:
	case <-timer.C:
		return nil, ErrHashingBusy
	}
	defer func() { <-sem }()
	return argonIDFunc([]byte(secret), salt, argonTime, argonMemory, argonThreads, argonKeyLen), nil
}
