package orchestrator

import (
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

type countingLogStore struct {
	storage.LogStore
	streams atomic.Int64
}

func (s *countingLogStore) Stream(context.Context, string, string) (io.ReadCloser, error) {
	s.streams.Add(1)
	return io.NopCloser(strings.NewReader("")), nil
}

func TestStreamNode_PausesBetweenReconnects(t *testing.T) {
	const window = 600 * time.Millisecond
	ls := &countingLogStore{}
	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()

	var mu sync.Mutex
	streamNode(ctx, ls, "run-follow", "n1", nil, &mu, io.Discard)

	got := ls.streams.Load()
	if got == 0 {
		t.Fatalf("streamNode never opened the stream")
	}
	if want := int64(window/(250*time.Millisecond)) + 2; got > want {
		t.Fatalf("streamNode reopened a promptly-closed stream %d times in %s, want at most %d", got, window, want)
	}
}
