package s3state_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A bucket that lists its keys and then refuses to serve them must not read as
// a pipeline that has never run: Ref.TryGet treats store.ErrNotFound as the
// bootstrap case and would rebuild everything for the length of the outage.
func TestS3StateBackend_GetLatestRun_ReportsAnUnreadableRecord(t *testing.T) {
	art := newMemArt()
	b := s3state.New(art, s3state.WithFlushInterval(5*time.Millisecond))
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()

	if err := b.CreateRun(ctx, store.Run{
		ID: "deploy-1", Pipeline: "deploy", Status: "success", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}

	outage := errors.New("s3: connection reset by peer")
	art.mu.Lock()
	art.getErr = outage
	art.mu.Unlock()

	reader := s3state.New(art)
	t.Cleanup(func() { _ = reader.Close() })

	_, err := reader.GetLatestRun(ctx, "deploy", []string{"success"}, time.Hour)
	if !errors.Is(err, outage) {
		t.Fatalf("GetLatestRun err = %v, want the read failure", err)
	}
	if errors.Is(err, store.ErrNotFound) {
		t.Fatal("an unreadable record was reported as no matching run")
	}
}
