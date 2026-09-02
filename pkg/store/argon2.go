package store

import (
	"sync"

	"golang.org/x/crypto/argon2"
)

// DefaultArgon2MemoryBudget is the resident memory ceiling for
// concurrent argon2id hashing. Each hash holds argonMemory KiB while
// it runs, so the default admits four at a time.
const DefaultArgon2MemoryBudget = int64(256) << 20

// Argon2HashBytes is the memory one argon2id hash holds while it runs.
const Argon2HashBytes = int64(argonMemory) << 10

var argonIDKey = argon2.IDKey

var (
	argonSemMu sync.RWMutex
	argonSem   = make(chan struct{}, Argon2Concurrency(DefaultArgon2MemoryBudget))
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

// SetArgon2MemoryBudget bounds concurrent argon2id hashing to what
// fits in budget bytes and returns the resulting concurrency. Hashes
// already running are unaffected; they release against the semaphore
// they acquired. A non-positive budget leaves the bound unchanged.
func SetArgon2MemoryBudget(budget int64) int {
	if budget <= 0 {
		argonSemMu.RLock()
		defer argonSemMu.RUnlock()
		return cap(argonSem)
	}
	slots := Argon2Concurrency(budget)
	argonSemMu.Lock()
	defer argonSemMu.Unlock()
	argonSem = make(chan struct{}, slots)
	return slots
}

func argonKey(secret string, salt []byte) []byte {
	argonSemMu.RLock()
	sem := argonSem
	argonSemMu.RUnlock()
	// safety: an unauthenticated caller drives these hashes, so the semaphore caps resident hashing memory.
	sem <- struct{}{}
	defer func() { <-sem }()
	return argonIDKey([]byte(secret), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
}
