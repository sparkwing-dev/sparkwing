package conformance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// TestConditionalWriter runs the conformance suite for the optional
// [storage.ConditionalWriter] capability against the store returned by
// factory. factory must return a fresh, empty store for each call.
//
// If the store does not implement [storage.ConditionalWriter], or its
// ConditionalWritesSupported probe reports false, the whole suite is
// skipped (not failed): the contract is that callers detect the
// missing capability and fall back to last-write-wins.
func TestConditionalWriter(t *testing.T, factory func() storage.ArtifactStore) {
	t.Helper()
	ctx := context.Background()

	cw, ok := storage.Conditional(factory())
	if !ok {
		t.Skip("store does not implement storage.ConditionalWriter")
	}
	supported, err := cw.ConditionalWritesSupported(ctx)
	if err != nil {
		t.Fatalf("ConditionalWritesSupported: %v", err)
	}
	if !supported {
		t.Skip("endpoint does not enforce write preconditions")
	}

	cond := func() storage.ConditionalWriter {
		c, ok := storage.Conditional(factory())
		if !ok {
			t.Fatalf("factory store stopped implementing ConditionalWriter")
		}
		return c
	}

	t.Run("PutIfAbsentCreatesWhenMissing", func(t *testing.T) {
		c := cond()
		etag, err := c.PutIfAbsent(ctx, "k", bytes.NewReader([]byte("v1")))
		if err != nil {
			t.Fatalf("PutIfAbsent: %v", err)
		}
		if etag == "" {
			t.Fatalf("PutIfAbsent returned empty ETag")
		}
		if got := readCond(t, c, ctx, "k"); string(got) != "v1" {
			t.Fatalf("Get = %q, want v1", got)
		}
	})

	t.Run("PutIfAbsentFailsWhenPresent", func(t *testing.T) {
		c := cond()
		if _, err := c.PutIfAbsent(ctx, "k", bytes.NewReader([]byte("first"))); err != nil {
			t.Fatalf("seed PutIfAbsent: %v", err)
		}
		_, err := c.PutIfAbsent(ctx, "k", bytes.NewReader([]byte("second")))
		if !errors.Is(err, storage.ErrPreconditionFailed) {
			t.Fatalf("PutIfAbsent over existing key: err = %v, want ErrPreconditionFailed", err)
		}
		if got := readCond(t, c, ctx, "k"); string(got) != "first" {
			t.Fatalf("failed PutIfAbsent overwrote: Get = %q, want first", got)
		}
	})

	t.Run("PutIfMatchSwapsOnMatch", func(t *testing.T) {
		c := cond()
		first, err := c.PutIfAbsent(ctx, "k", bytes.NewReader([]byte("v1")))
		if err != nil {
			t.Fatalf("seed PutIfAbsent: %v", err)
		}
		next, err := c.PutIfMatch(ctx, "k", bytes.NewReader([]byte("v2")), first)
		if err != nil {
			t.Fatalf("PutIfMatch on current ETag: %v", err)
		}
		if next == first {
			t.Fatalf("ETag did not change after write: %q", next)
		}
		if got := readCond(t, c, ctx, "k"); string(got) != "v2" {
			t.Fatalf("Get = %q, want v2", got)
		}
	})

	t.Run("PutIfMatchFailsOnStaleETag", func(t *testing.T) {
		c := cond()
		first, err := c.PutIfAbsent(ctx, "k", bytes.NewReader([]byte("v1")))
		if err != nil {
			t.Fatalf("seed PutIfAbsent: %v", err)
		}
		if _, err := c.PutIfMatch(ctx, "k", bytes.NewReader([]byte("v2")), first); err != nil {
			t.Fatalf("advance PutIfMatch: %v", err)
		}
		_, err = c.PutIfMatch(ctx, "k", bytes.NewReader([]byte("v3")), first)
		if !errors.Is(err, storage.ErrPreconditionFailed) {
			t.Fatalf("PutIfMatch on stale ETag: err = %v, want ErrPreconditionFailed", err)
		}
		if got := readCond(t, c, ctx, "k"); string(got) != "v2" {
			t.Fatalf("failed PutIfMatch overwrote: Get = %q, want v2", got)
		}
	})

	t.Run("PutIfMatchFailsWhenAbsent", func(t *testing.T) {
		c := cond()
		_, err := c.PutIfMatch(ctx, "never", bytes.NewReader([]byte("v")), storage.ETag("anything"))
		if !errors.Is(err, storage.ErrPreconditionFailed) {
			t.Fatalf("PutIfMatch on absent key: err = %v, want ErrPreconditionFailed", err)
		}
	})

	t.Run("GetWithETagMissingReturnsNotFound", func(t *testing.T) {
		c := cond()
		_, _, err := c.GetWithETag(ctx, "never-written")
		if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("GetWithETag on missing key: err = %v, want ErrNotFound", err)
		}
	})

	t.Run("GetWithETagMatchesPutETag", func(t *testing.T) {
		c := cond()
		put, err := c.PutIfAbsent(ctx, "k", bytes.NewReader([]byte("v1")))
		if err != nil {
			t.Fatalf("PutIfAbsent: %v", err)
		}
		rc, got, err := c.GetWithETag(ctx, "k")
		if err != nil {
			t.Fatalf("GetWithETag: %v", err)
		}
		_ = rc.Close()
		if got != put {
			t.Fatalf("GetWithETag ETag = %q, want %q from PutIfAbsent", got, put)
		}
	})

	t.Run("ReadModifyWriteLoopConverges", func(t *testing.T) {
		c := cond()
		if _, err := c.PutIfAbsent(ctx, "counter", bytes.NewReader([]byte("0"))); err != nil {
			t.Fatalf("seed: %v", err)
		}
		rc, etag, err := c.GetWithETag(ctx, "counter")
		if err != nil {
			t.Fatalf("GetWithETag: %v", err)
		}
		cur, _ := io.ReadAll(rc)
		_ = rc.Close()
		if string(cur) != "0" {
			t.Fatalf("counter = %q, want 0", cur)
		}
		if _, err := c.PutIfMatch(ctx, "counter", bytes.NewReader([]byte("1")), etag); err != nil {
			t.Fatalf("CAS write: %v", err)
		}
		if got := readCond(t, c, ctx, "counter"); string(got) != "1" {
			t.Fatalf("counter = %q, want 1", got)
		}
	})
}

func readCond(t *testing.T, c storage.ConditionalWriter, ctx context.Context, key string) []byte {
	t.Helper()
	rc, _, err := c.GetWithETag(ctx, key)
	if err != nil {
		t.Fatalf("GetWithETag(%q): %v", key, err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll(%q): %v", key, err)
	}
	return body
}
