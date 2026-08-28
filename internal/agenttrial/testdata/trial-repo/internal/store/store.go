package store

import (
	"errors"
	"sync"
)

var ErrNotFound = errors.New("store: not found")

type Account struct {
	ID    string
	Email string
}

type Store struct {
	mu       sync.RWMutex
	accounts map[string]Account
}

func New() *Store {
	return &Store{accounts: make(map[string]Account)}
}

func (s *Store) Put(a Account) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accounts[a.ID] = a
}

func (s *Store) Get(id string) (Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.accounts[id]
	if !ok {
		return Account{}, ErrNotFound
	}
	return a, nil
}

func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.accounts)
}
