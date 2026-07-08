package conformance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// TestLogStore runs the conformance suite for [storage.LogStore]
// against the implementation returned by factory. factory must
// return a fresh, empty store for each call.
//
// Subtests that hit a read path the implementation has opted out of
// (returning an error that wraps [storage.ErrNotSupported]) are
// skipped, not failed -- this lets write-only stores like
// [stdoutlogs.LogStore] still run the suite for the Append path.
func TestLogStore(t *testing.T, factory func() storage.LogStore) {
	t.Helper()

	t.Run("AppendReadRoundTrip", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		mustAppend(t, s, ctx, "run-1", "build", "alpha\nbeta\ngamma\n")
		body, err := s.Read(ctx, "run-1", "build", storage.ReadOpts{})
		if maybeSkipUnsupported(t, err, "Read") {
			return
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if !strings.Contains(string(body), "alpha") ||
			!strings.Contains(string(body), "beta") ||
			!strings.Contains(string(body), "gamma") {
			t.Fatalf("Read returned %q, missing one of alpha/beta/gamma", body)
		}
	})

	t.Run("ReadEmptyNodeReturnsNoBytesNoError", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		body, err := s.Read(ctx, "run-1", "never-appended", storage.ReadOpts{})
		if maybeSkipUnsupported(t, err, "Read") {
			return
		}
		if err != nil {
			t.Fatalf("Read on empty node returned error: %v", err)
		}
		if len(body) != 0 {
			t.Fatalf("Read on empty node returned %d bytes, want 0", len(body))
		}
	})

	t.Run("ReadWithTail", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		mustAppend(t, s, ctx, "run-1", "build", "a\nb\nc\nd\ne\n")
		body, err := s.Read(ctx, "run-1", "build", storage.ReadOpts{Tail: 2})
		if maybeSkipUnsupported(t, err, "Read") {
			return
		}
		if err != nil {
			t.Fatalf("Read with Tail=2: %v", err)
		}
		s2 := strings.TrimRight(string(body), "\n")
		lines := strings.Split(s2, "\n")
		if len(lines) != 2 || lines[0] != "d" || lines[1] != "e" {
			t.Fatalf("Tail=2 returned %q, want last two lines d,e", body)
		}
	})

	t.Run("ReadWithHead", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		mustAppend(t, s, ctx, "run-1", "build", "a\nb\nc\nd\ne\n")
		body, err := s.Read(ctx, "run-1", "build", storage.ReadOpts{Head: 2})
		if maybeSkipUnsupported(t, err, "Read") {
			return
		}
		if err != nil {
			t.Fatalf("Read with Head=2: %v", err)
		}
		s2 := strings.TrimRight(string(body), "\n")
		lines := strings.Split(s2, "\n")
		if len(lines) != 2 || lines[0] != "a" || lines[1] != "b" {
			t.Fatalf("Head=2 returned %q, want first two lines a,b", body)
		}
	})

	t.Run("ReadWithGrep", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		mustAppend(t, s, ctx, "run-1", "build", "hello\nworld\nhello again\nbye\n")
		body, err := s.Read(ctx, "run-1", "build", storage.ReadOpts{Grep: "hello"})
		if maybeSkipUnsupported(t, err, "Read") {
			return
		}
		if err != nil {
			t.Fatalf("Read with Grep=hello: %v", err)
		}
		text := string(body)
		if !strings.Contains(text, "hello") {
			t.Fatalf("Grep returned %q, expected lines containing hello", body)
		}
		if strings.Contains(text, "bye") || strings.Contains(text, "world") {
			t.Fatalf("Grep returned %q, expected no non-matching lines", body)
		}
	})

	t.Run("ReadRunReturnsAllNodes", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		mustAppend(t, s, ctx, "run-1", "build", "build-line\n")
		mustAppend(t, s, ctx, "run-1", "test", "test-line\n")
		body, err := s.ReadRun(ctx, "run-1")
		if maybeSkipUnsupported(t, err, "ReadRun") {
			return
		}
		if err != nil {
			t.Fatalf("ReadRun: %v", err)
		}
		text := string(body)
		if !strings.Contains(text, "build-line") || !strings.Contains(text, "test-line") {
			t.Fatalf("ReadRun returned %q, expected both build-line and test-line", body)
		}
	})

	t.Run("Stream", func(t *testing.T) {
		s := factory()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		mustAppend(t, s, ctx, "run-1", "build", "first\n")
		rc, err := s.Stream(ctx, "run-1", "build")
		if maybeSkipUnsupported(t, err, "Stream") {
			return
		}
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		if rc == nil {
			t.Skip("Stream returned (nil, nil); implementation opts out")
		}
		defer rc.Close()

		go func() {
			time.Sleep(10 * time.Millisecond)
			_ = s.Append(ctx, "run-1", "build", []byte("second\n"))
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()
		body, _ := io.ReadAll(rc)
		if !bytes.Contains(body, []byte("first")) && !bytes.Contains(body, []byte("second")) {
			t.Fatalf("Stream returned %q, expected to see first or second", body)
		}
	})

	t.Run("DeleteRunRemovesLogs", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		mustAppend(t, s, ctx, "run-1", "build", "alpha\n")
		if err := s.DeleteRun(ctx, "run-1"); err != nil {
			t.Fatalf("DeleteRun: %v", err)
		}
		body, err := s.Read(ctx, "run-1", "build", storage.ReadOpts{})
		if maybeSkipUnsupported(t, err, "Read") {
			return
		}
		if err != nil {
			t.Fatalf("Read after DeleteRun: %v", err)
		}
		if len(body) != 0 {
			t.Fatalf("Read after DeleteRun returned %q, want empty", body)
		}
	})

	t.Run("DeleteRunMissingIsIdempotent", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		if err := s.DeleteRun(ctx, "never-existed"); err != nil {
			t.Fatalf("DeleteRun of missing run returned error: %v", err)
		}
	})

	t.Run("AppendAfterDeleteWorks", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		mustAppend(t, s, ctx, "run-1", "build", "first\n")
		if err := s.DeleteRun(ctx, "run-1"); err != nil {
			t.Fatalf("DeleteRun: %v", err)
		}
		mustAppend(t, s, ctx, "run-1", "build", "second\n")
		body, err := s.Read(ctx, "run-1", "build", storage.ReadOpts{})
		if maybeSkipUnsupported(t, err, "Read") {
			return
		}
		if err != nil {
			t.Fatalf("Read after re-append: %v", err)
		}
		if !strings.Contains(string(body), "second") {
			t.Fatalf("Read after re-append returned %q, expected to contain second", body)
		}
		if strings.Contains(string(body), "first") {
			t.Fatalf("Read after re-append returned %q, expected DeleteRun to have removed first", body)
		}
	})
}

func mustAppend(t *testing.T, s storage.LogStore, ctx context.Context, runID, nodeID, payload string) {
	t.Helper()
	if err := s.Append(ctx, runID, nodeID, []byte(payload)); err != nil {
		t.Fatalf("Append(%s/%s): %v", runID, nodeID, err)
	}
}

// silence unused-import warnings on builds that strip unreachable
// branches when errors helpers aren't referenced.
var _ = errors.Is
