package main

import (
	"context"
	"errors"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type admissionStore struct {
	path string
	once sync.Once
	db   *store.Store
	err  error
}

func (s *admissionStore) LoadMachineAdmission(ctx context.Context) ([]byte, error) {
	s.once.Do(func() { s.db, s.err = store.Open(s.path) }) //nolint:contextcheck // safety: election owns initialization; Open has no context parameter.
	if s.err != nil {
		return nil, s.err
	}
	return s.db.LoadMachineAdmission(ctx)
}

func (s *admissionStore) SaveMachineAdmission(ctx context.Context, state []byte) error {
	if s.db == nil {
		return errors.New("admission state must be loaded before saving")
	}
	return s.db.SaveMachineAdmission(ctx, state)
}

func (s *admissionStore) close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}
