// Package store holds accounts and invoices in memory. A real service
// would talk to Postgres; this keeps the fixture dependency-free while
// still giving the test suite something with behavior to check.
package store

import (
	"errors"
	"sync"
)

// ErrNotFound is returned when a lookup misses.
var ErrNotFound = errors.New("store: not found")

// Account is a billable customer.
type Account struct {
	ID    string
	Email string
}

// Store is a goroutine-safe account table.
type Store struct {
	mu       sync.RWMutex
	accounts map[string]Account
}

// New returns an empty Store.
func New() *Store {
	return &Store{accounts: make(map[string]Account)}
}

// Put inserts or replaces an account.
func (s *Store) Put(a Account) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accounts[a.ID] = a
}

// Get returns the account with id, or ErrNotFound.
func (s *Store) Get(id string) (Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.accounts[id]
	if !ok {
		return Account{}, ErrNotFound
	}
	return a, nil
}

// Len reports how many accounts are stored.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.accounts)
}
