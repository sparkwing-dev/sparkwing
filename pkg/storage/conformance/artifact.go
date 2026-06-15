package conformance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// TestArtifactStore runs the conformance suite for
// [storage.ArtifactStore] against the implementation returned by
// factory. factory must return a fresh, empty store for each call.
//
// Subtests that hit an operation the implementation has opted out of
// (returning an error that wraps [storage.ErrNotSupported] or
// [storage.ErrListNotSupported]) are skipped, not failed.
func TestArtifactStore(t *testing.T, factory func() storage.ArtifactStore) {
	t.Helper()
	t.Run("PutGetRoundTrip", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		body := []byte("hello sparkwing")
		mustPut(t, s, ctx, "alpha", body)
		got := mustGet(t, s, ctx, "alpha")
		if !bytes.Equal(got, body) {
			t.Fatalf("Get returned %q, want %q", got, body)
		}
	})

	t.Run("PutOverwriteLastWins", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		mustPut(t, s, ctx, "k", []byte("first"))
		mustPut(t, s, ctx, "k", []byte("second"))
		got := mustGet(t, s, ctx, "k")
		if string(got) != "second" {
			t.Fatalf("overwrite returned %q, want %q", got, "second")
		}
	})

	t.Run("HasAfterPut", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		mustPut(t, s, ctx, "k", []byte("v"))
		has, err := s.Has(ctx, "k")
		if err != nil {
			t.Fatalf("Has: %v", err)
		}
		if !has {
			t.Fatalf("Has returned false after Put")
		}
	})

	t.Run("HasMissingReturnsFalseNoError", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		has, err := s.Has(ctx, "never-written")
		if err != nil {
			t.Fatalf("Has on missing key returned error: %v", err)
		}
		if has {
			t.Fatalf("Has returned true for missing key")
		}
	})

	t.Run("GetMissingReturnsErrNotFound", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		_, err := s.Get(ctx, "never-written")
		if err == nil {
			t.Fatalf("Get on missing key returned nil error")
		}
		if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("Get missing: err = %v, want errors.Is(err, storage.ErrNotFound)", err)
		}
	})

	t.Run("DeleteRemovesKey", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		mustPut(t, s, ctx, "k", []byte("v"))
		if err := s.Delete(ctx, "k"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		has, _ := s.Has(ctx, "k")
		if has {
			t.Fatalf("Has returned true after Delete")
		}
		_, err := s.Get(ctx, "k")
		if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("Get after Delete: err = %v, want ErrNotFound", err)
		}
	})

	t.Run("DeleteMissingIsIdempotent", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		if err := s.Delete(ctx, "never-written"); err != nil {
			t.Fatalf("Delete of missing key returned error: %v", err)
		}
	})

	t.Run("ListEmptyReturnsNoEntries", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		got, err := s.List(ctx, "")
		if maybeSkipUnsupported(t, err, "List") {
			return
		}
		if err != nil {
			t.Fatalf("List on empty store: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("List on empty store returned %v, want empty", got)
		}
	})

	t.Run("ListPrefixFiltersKeys", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		mustPut(t, s, ctx, "build/abc", []byte("1"))
		mustPut(t, s, ctx, "build/def", []byte("2"))
		mustPut(t, s, ctx, "test/ghi", []byte("3"))
		got, err := s.List(ctx, "build/")
		if maybeSkipUnsupported(t, err, "List") {
			return
		}
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		sort.Strings(got)
		for _, k := range got {
			if !strings.HasPrefix(k, "build/") {
				t.Fatalf("List(build/) returned non-matching key %q", k)
			}
		}
		seen := map[string]bool{}
		for _, k := range got {
			seen[k] = true
		}
		if !seen["build/abc"] || !seen["build/def"] {
			t.Fatalf("List(build/) returned %v, want both build/abc and build/def", got)
		}
		if seen["test/ghi"] {
			t.Fatalf("List(build/) returned non-matching key test/ghi")
		}
	})

	t.Run("PutEmptyPayload", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		mustPut(t, s, ctx, "empty", []byte{})
		got := mustGet(t, s, ctx, "empty")
		if len(got) != 0 {
			t.Fatalf("Get of empty payload returned %d bytes, want 0", len(got))
		}
	})
}

func mustPut(t *testing.T, s storage.ArtifactStore, ctx context.Context, key string, body []byte) {
	t.Helper()
	if err := s.Put(ctx, key, bytes.NewReader(body)); err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
}

func mustGet(t *testing.T, s storage.ArtifactStore, ctx context.Context, key string) []byte {
	t.Helper()
	r, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	defer r.Close()
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll(Get(%q)): %v", key, err)
	}
	return body
}

// maybeSkipUnsupported returns true if err signals the implementation
// has opted out of this operation (per the conformance contract).
// Logs the skip with the wrapped reason so test output still names
// what was skipped and why.
func maybeSkipUnsupported(t *testing.T, err error, op string) bool {
	t.Helper()
	if err == nil {
		return false
	}
	if errors.Is(err, storage.ErrNotSupported) || errors.Is(err, storage.ErrListNotSupported) {
		t.Skipf("%s not supported by this implementation: %v", op, err)
		return true
	}
	return false
}
