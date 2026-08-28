package s3state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/smithy-go"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

type lockedLogBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedLogBuffer) entries(t *testing.T, message string) []map[string]any {
	t.Helper()
	b.mu.Lock()
	body := b.b.String()
	b.mu.Unlock()

	var entries []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode log entry %q: %v", line, err)
		}
		if entry["msg"] == message {
			entries = append(entries, entry)
		}
	}
	return entries
}

type controlledArtifactStore struct {
	mu       sync.Mutex
	putError func(int) error
	attempts []string
	data     map[string][]byte
}

func newControlledArtifactStore() *controlledArtifactStore {
	return &controlledArtifactStore{data: make(map[string][]byte)}
}

func (s *controlledArtifactStore) setPutError(err error) {
	s.setPutErrorFactory(func(int) error { return err })
}

func (s *controlledArtifactStore) setPutErrorFactory(factory func(int) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putError = factory
}

func (s *controlledArtifactStore) attemptCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.attempts)
}

func (s *controlledArtifactStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.data[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (s *controlledArtifactStore) Put(_ context.Context, key string, r io.Reader) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, key)
	if s.putError != nil {
		if err := s.putError(len(s.attempts)); err != nil {
			return err
		}
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.data[key] = body
	return nil
}

func (s *controlledArtifactStore) Has(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[key]
	return ok, nil
}

func (s *controlledArtifactStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

func (s *controlledArtifactStore) List(context.Context, string) ([]string, error) {
	return nil, storage.ErrListNotSupported
}

type awsShapedError struct {
	status    int
	code      string
	message   string
	requestID string
	hostID    string
}

func (e *awsShapedError) Error() string {
	return fmt.Sprintf(
		"https response error StatusCode: %d, RequestID: %s, HostID: %s, api error %s: %s",
		e.status, e.requestID, e.hostID, e.code, e.message,
	)
}

func (e *awsShapedError) Unwrap() error {
	return &smithy.GenericAPIError{Code: e.code, Message: e.message, Fault: smithy.FaultClient}
}

func (e *awsShapedError) HTTPStatusCode() int { return e.status }

func awsPutErrorFactory(status int, code, message string) func(int) error {
	return func(attempt int) error {
		return &awsShapedError{
			status:    status,
			code:      code,
			message:   message,
			requestID: fmt.Sprintf("request-%d", attempt),
			hostID:    fmt.Sprintf("host-%d", attempt),
		}
	}
}

type httpStatusOnlyError struct {
	status    int
	requestID string
	hostID    string
	cause     string
}

func (e *httpStatusOnlyError) Error() string {
	return fmt.Sprintf(
		"https response error StatusCode: %d, RequestID: %s, HostID: %s, %s",
		e.status, e.requestID, e.hostID, e.cause,
	)
}

func (e *httpStatusOnlyError) Unwrap() error       { return errors.New(e.cause) }
func (e *httpStatusOnlyError) HTTPStatusCode() int { return e.status }

func statusOnlyPutErrorFactory(status int, cause string) func(int) error {
	return func(attempt int) error {
		return &httpStatusOnlyError{
			status:    status,
			requestID: fmt.Sprintf("request-%d", attempt),
			hostID:    fmt.Sprintf("host-%d", attempt),
			cause:     cause,
		}
	}
}

func waitForOutboxCondition(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for outbox condition")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestOutboxBackgroundDrainReportsTransitionsWithoutRetrySpam(t *testing.T) {
	logs := &lockedLogBuffer{}
	art := newControlledArtifactStore()
	art.setPutErrorFactory(awsPutErrorFactory(403, "AccessDenied", "access denied"))
	outbox, err := openOutbox(
		filepath.Join(t.TempDir(), "outbox.db"),
		art,
		5*time.Millisecond,
		slog.New(slog.NewJSONHandler(logs, nil)),
	)
	if err != nil {
		t.Fatalf("openOutbox: %v", err)
	}
	t.Cleanup(func() { _ = outbox.Close() })

	ctx := context.Background()
	key := "runs/r/state.ndjson"
	if err := outbox.Stage(ctx, OutboxKindState, key, []byte("body")); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	waitForOutboxCondition(t, func() bool {
		return len(logs.entries(t, "s3 state outbox replay stalled")) == 1
	})
	waitForOutboxCondition(t, func() bool { return art.attemptCount() >= 4 })
	stalls := logs.entries(t, "s3 state outbox replay stalled")
	if len(stalls) != 1 {
		t.Fatalf("stall warnings after identical retries = %d, want 1", len(stalls))
	}
	if stalls[0]["kind"] != string(OutboxKindState) || stalls[0]["key"] != key {
		t.Fatalf("stall context = kind %v, key %v", stalls[0]["kind"], stalls[0]["key"])
	}
	if got := stalls[0]["error"].(string); !strings.Contains(got, "RequestID: request-1") ||
		!strings.Contains(got, "HostID: host-1") || !strings.Contains(got, "api error AccessDenied: access denied") {
		t.Fatalf("stall error = %q, want the complete current AWS error", got)
	}
	if pending, err := outbox.Pending(ctx); err != nil || pending != 1 {
		t.Fatalf("Pending after retries = %d, %v; want 1, nil", pending, err)
	}

	art.setPutErrorFactory(awsPutErrorFactory(403, "AccessDenied", "policy denies write"))
	waitForOutboxCondition(t, func() bool {
		return len(logs.entries(t, "s3 state outbox replay stalled")) == 2
	})
	stalls = logs.entries(t, "s3 state outbox replay stalled")
	if got := stalls[1]["error"].(string); !strings.Contains(got, "api error AccessDenied: policy denies write") {
		t.Fatalf("message-transition error = %q", got)
	}

	art.setPutErrorFactory(awsPutErrorFactory(403, "ExpiredToken", "policy denies write"))
	waitForOutboxCondition(t, func() bool {
		return len(logs.entries(t, "s3 state outbox replay stalled")) == 3
	})

	art.setPutErrorFactory(awsPutErrorFactory(503, "ExpiredToken", "policy denies write"))
	waitForOutboxCondition(t, func() bool {
		return len(logs.entries(t, "s3 state outbox replay stalled")) == 4
	})
	stalls = logs.entries(t, "s3 state outbox replay stalled")
	if got := stalls[3]["error"].(string); !strings.Contains(got, "StatusCode: 503") ||
		!strings.Contains(got, "RequestID:") || !strings.Contains(got, "HostID:") {
		t.Fatalf("status-transition error = %q, want the complete current AWS error", got)
	}

	art.setPutError(nil)
	waitForOutboxCondition(t, func() bool {
		pending, err := outbox.Pending(ctx)
		return err == nil && pending == 0 && len(logs.entries(t, "s3 state outbox replay recovered")) == 1
	})
	for range 3 {
		outbox.reportDrainResult(outboxHead{}, nil)
	}
	recoveries := logs.entries(t, "s3 state outbox replay recovered")
	if len(recoveries) != 1 {
		t.Fatalf("recovery notices = %d, want 1", len(recoveries))
	}
	if recoveries[0]["kind"] != string(OutboxKindState) || recoveries[0]["key"] != key {
		t.Fatalf("recovery context = kind %v, key %v", recoveries[0]["kind"], recoveries[0]["key"])
	}
}

func TestOutboxErrorFingerprintFallsBackForNonSmithyErrors(t *testing.T) {
	base := fingerprintOutboxError(errors.New("connection refused"))
	if same := fingerprintOutboxError(errors.New("connection refused")); same != base {
		t.Fatalf("same non-Smithy error fingerprint = %+v, want %+v", same, base)
	}
	if changed := fingerprintOutboxError(errors.New("permission denied")); changed == base {
		t.Fatalf("changed non-Smithy error fingerprint = %+v, want a transition", changed)
	}
}

func TestOutboxBackgroundDrainStatusOnlyFingerprintUsesUnderlyingCause(t *testing.T) {
	logs := &lockedLogBuffer{}
	art := newControlledArtifactStore()
	art.setPutErrorFactory(statusOnlyPutErrorFactory(403, "gateway refused write"))
	outbox, err := openOutbox(
		filepath.Join(t.TempDir(), "outbox.db"),
		art,
		5*time.Millisecond,
		slog.New(slog.NewJSONHandler(logs, nil)),
	)
	if err != nil {
		t.Fatalf("openOutbox: %v", err)
	}
	t.Cleanup(func() { _ = outbox.Close() })

	ctx := context.Background()
	if err := outbox.Stage(ctx, OutboxKindState, "runs/r/state.ndjson", []byte("body")); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	waitForOutboxCondition(t, func() bool {
		return len(logs.entries(t, "s3 state outbox replay stalled")) == 1
	})
	waitForOutboxCondition(t, func() bool { return art.attemptCount() >= 4 })
	if got := len(logs.entries(t, "s3 state outbox replay stalled")); got != 1 {
		t.Fatalf("status-only warnings across volatile request metadata = %d, want 1", got)
	}

	art.setPutErrorFactory(statusOnlyPutErrorFactory(403, "bucket policy denied write"))
	waitForOutboxCondition(t, func() bool {
		return len(logs.entries(t, "s3 state outbox replay stalled")) == 2
	})
	attemptsAfterTransition := art.attemptCount()
	waitForOutboxCondition(t, func() bool { return art.attemptCount() >= attemptsAfterTransition+3 })
	stalls := logs.entries(t, "s3 state outbox replay stalled")
	if len(stalls) != 2 {
		t.Fatalf("status-only warnings after unchanged cause retries = %d, want 2", len(stalls))
	}
	if got := stalls[1]["error"].(string); !strings.Contains(got, "StatusCode: 403") ||
		!strings.Contains(got, "RequestID:") || !strings.Contains(got, "HostID:") ||
		!strings.Contains(got, "bucket policy denied write") {
		t.Fatalf("status-only transition error = %q, want the complete current error", got)
	}

	art.setPutError(nil)
	waitForOutboxCondition(t, func() bool {
		pending, err := outbox.Pending(ctx)
		return err == nil && pending == 0
	})
}

func TestOutboxManualDrainDoesNotEmitBackgroundTransitionLogs(t *testing.T) {
	logs := &lockedLogBuffer{}
	art := newControlledArtifactStore()
	putErr := errors.New("access denied")
	art.setPutError(putErr)
	outbox, err := openOutbox(
		filepath.Join(t.TempDir(), "outbox.db"),
		art,
		time.Hour,
		slog.New(slog.NewJSONHandler(logs, nil)),
	)
	if err != nil {
		t.Fatalf("openOutbox: %v", err)
	}
	t.Cleanup(func() { _ = outbox.Close() })

	ctx := context.Background()
	if err := outbox.Stage(ctx, OutboxKindArtifact, "artifacts/a", []byte("body")); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := outbox.Drain(ctx); !errors.Is(err, putErr) {
		t.Fatalf("Drain error = %v, want %v", err, putErr)
	}
	if got := len(logs.entries(t, "s3 state outbox replay stalled")); got != 0 {
		t.Fatalf("manual Drain emitted %d background stall warnings, want 0", got)
	}
}
