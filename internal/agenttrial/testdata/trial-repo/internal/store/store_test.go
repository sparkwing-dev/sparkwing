package store

import (
	"errors"
	"sync"
	"testing"
)

func TestPutGet(t *testing.T) {
	s := New()
	s.Put(Account{ID: "a1", Email: "a@example.com"})

	got, err := s.Get("a1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Email != "a@example.com" {
		t.Errorf("Email = %q", got.Email)
	}
	if _, err := s.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestPutReplaces(t *testing.T) {
	s := New()
	s.Put(Account{ID: "a1", Email: "first@example.com"})
	s.Put(Account{ID: "a1", Email: "second@example.com"})
	if s.Len() != 1 {
		t.Fatalf("Len = %d, want 1", s.Len())
	}
	got, _ := s.Get("a1")
	if got.Email != "second@example.com" {
		t.Errorf("Email = %q, want the replacement", got.Email)
	}
}

func TestConcurrentPut(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.Put(Account{ID: string(rune('a' + i%26))})
		}(i)
	}
	wg.Wait()
	if s.Len() == 0 {
		t.Error("expected accounts after concurrent writes")
	}
}
