package store

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestFlowNoncesAreOneUseAndKindScopedProperty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonces.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	now := time.Unix(1700000000, 123456789).UTC()
	s.clock = func() time.Time { return now }
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s.SetSessionKeyCipher(cipher)
	key, err := s.IdentityLinkStateKey(t.Context())
	if err != nil || len(key) == 0 {
		t.Fatalf("identity link key: %v", err)
	}
	for i := range 24 {
		nonce := fmt.Sprintf("%d/'雪\x00", i)
		expires := now.Add(time.Duration(i+1) * time.Nanosecond)
		for _, consume := range []func(string, time.Time, time.Time) (bool, error){
			func(n string, e, at time.Time) (bool, error) {
				return s.consumeFlowNonce(t.Context(), "identity_link", n, e)
			},
			func(n string, e, at time.Time) (bool, error) {
				return s.consumeFlowNonce(t.Context(), "github_app_connect", n, e)
			},
		} {
			if ok, err := consume(nonce, expires, now); err != nil || !ok {
				t.Fatalf("first use: %v, %v", ok, err)
			}
			if ok, err := consume(nonce, expires, now); err != nil || ok {
				t.Fatalf("replay: %v, %v", ok, err)
			}
			if ok, err := consume(nonce, expires, now.Add(time.Hour)); err != nil || ok {
				t.Fatalf("caller clock changed replay: %v, %v", ok, err)
			}
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.clock = func() time.Time { return now }
	s.SetSessionKeyCipher(cipher)
	reopenedKey, err := s.IdentityLinkStateKey(t.Context())
	if err != nil || !bytes.Equal(key, reopenedKey) {
		t.Fatalf("identity link key changed after reopening: %v", err)
	}

	if ok, err := s.consumeFlowNonce(t.Context(), "identity_link", "0/'雪\x00", now.Add(time.Nanosecond)); err != nil || ok {
		t.Fatalf("restart replay: %v, %v", ok, err)
	}
	for _, nonce := range []string{"", "expired"} {
		if ok, err := s.consumeFlowNonce(t.Context(), "identity_link", nonce, now); err != nil || ok {
			t.Fatalf("invalid flow consumed: %v, %v", ok, err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan bool, 8)
	failures := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			ok, err := s.consumeFlowNonce(t.Context(), "identity_link", "racing", now.Add(time.Minute))
			results <- ok
			failures <- err
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	winners := 0
	for ok := range results {
		if ok {
			winners++
		}
	}
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d concurrent consumers won", winners)
	}
}
