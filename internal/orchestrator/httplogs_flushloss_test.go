package orchestrator_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logbatch"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type refusingLogStore struct{}

func (refusingLogStore) Append(context.Context, string, string, []byte) error {
	return errors.New("bucket unreachable")
}

func (refusingLogStore) Read(context.Context, string, string, storage.ReadOpts) ([]byte, error) {
	return nil, nil
}
func (refusingLogStore) ReadRun(context.Context, string) ([]byte, error) { return nil, nil }
func (refusingLogStore) Stream(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
}
func (refusingLogStore) DeleteRun(context.Context, string) error { return nil }

var _ storage.LogStore = refusingLogStore{}

func TestNodeLogDrops_CountTheBatchAFailedFlushDiscarded(t *testing.T) {
	batcher := logbatch.New(refusingLogStore{},
		logbatch.WithFlushInterval(time.Hour),
		logbatch.WithBufferThreshold(1<<20))
	t.Cleanup(func() { _ = batcher.Close() })

	nlog, err := orchestrator.NewLogStoreBackend(batcher, nil).
		OpenNodeLog(context.Background(), "run-1", "build", nil)
	if err != nil {
		t.Fatalf("OpenNodeLog: %v", err)
	}
	for i := range 3 {
		nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: string(rune('a' + i))})
	}
	if err := nlog.Close(); err == nil {
		t.Fatal("Close succeeded against a store that refuses every write")
	}

	dropper, ok := nlog.(interface{ Drops() (int, string) })
	if !ok {
		t.Fatalf("node log %T should expose Drops()", nlog)
	}
	count, reason := dropper.Drops()
	if count < 3 {
		t.Fatalf("drops = %d, want at least the 3 buffered lines", count)
	}
	if !strings.Contains(reason, "failed log flush") {
		t.Fatalf("drop reason = %q, want it to name the failed flush", reason)
	}
}
