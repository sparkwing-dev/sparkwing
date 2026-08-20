package s3state_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// gatedArt holds the first Put open until the test releases it, which
// keeps one flush in flight for as long as an assertion needs without
// depending on load or timing to collide with the flush ticker.
type gatedArt struct {
	*memArt
	entered     chan string
	release     chan struct{}
	holdOnce    sync.Once
	releaseOnce sync.Once
}

func newGatedArt() *gatedArt {
	return &gatedArt{
		memArt:  newMemArt(),
		entered: make(chan string, 1),
		release: make(chan struct{}),
	}
}

func (g *gatedArt) Put(ctx context.Context, key string, r io.Reader) error {
	held := false
	g.holdOnce.Do(func() { held = true })
	if held {
		g.entered <- key
		<-g.release
	}
	return g.memArt.Put(ctx, key, r)
}

func (g *gatedArt) releaseHeldPut() { g.releaseOnce.Do(func() { close(g.release) }) }

func runningRun(id string) store.Run {
	return store.Run{ID: id, Pipeline: "p", Status: "running", StartedAt: time.Now().UTC()}
}

func TestBackend_EnvelopeAppendedDuringAFlushIsStillWrittenOnClose(t *testing.T) {
	art := newGatedArt()
	b := s3state.New(art, s3state.WithFlushInterval(5*time.Millisecond))
	t.Cleanup(func() { _ = b.Close() })
	t.Cleanup(art.releaseHeldPut)
	ctx := context.Background()

	if err := b.CreateRun(ctx, runningRun("r")); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	<-art.entered

	// safety: the held PUT carries a snapshot taken before this node existed,
	// so only a later flush can make the node durable.
	if err := b.CreateNode(ctx, store.Node{RunID: "r", NodeID: "n1", Status: "running"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}

	art.releaseHeldPut()
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reader := s3state.New(art)
	t.Cleanup(func() { _ = reader.Close() })
	if _, err := reader.GetNode(ctx, "r", "n1"); err != nil {
		t.Fatalf("node appended while a flush was in flight was never written: %v", err)
	}
}

func TestBackend_FinishRunFailsWhileTheInFlightFlushCannotLand(t *testing.T) {
	art := newGatedArt()
	b := s3state.New(art, s3state.WithFlushInterval(5*time.Millisecond))
	t.Cleanup(func() { _ = b.Close() })
	t.Cleanup(art.releaseHeldPut)
	ctx := context.Background()

	if err := b.CreateRun(ctx, runningRun("r")); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	<-art.entered

	finishCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	err := b.FinishRun(finishCtx, "r", "succeeded", "")

	written, herr := art.Has(ctx, "runs/r/state.ndjson")
	if herr != nil {
		t.Fatalf("Has: %v", herr)
	}
	if err == nil && !written {
		t.Fatal("FinishRun reported success with nothing written for the run")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("FinishRun error = %v, want context.DeadlineExceeded", err)
	}
}

func TestBackend_FinishRunPersistsAfterWaitingOutAnInFlightFlush(t *testing.T) {
	art := newGatedArt()
	b := s3state.New(art, s3state.WithFlushInterval(5*time.Millisecond))
	t.Cleanup(func() { _ = b.Close() })
	t.Cleanup(art.releaseHeldPut)
	ctx := context.Background()

	if err := b.CreateRun(ctx, runningRun("r")); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	<-art.entered

	var finishErr error
	finishDone := make(chan struct{})
	go func() {
		defer close(finishDone)
		finishErr = b.FinishRun(ctx, "r", "succeeded", "")
	}()
	t.Cleanup(func() {
		art.releaseHeldPut()
		join := time.NewTimer(time.Second)
		defer join.Stop()
		select {
		case <-finishDone:
		case <-join.C:
			t.Error("FinishRun did not stop during cleanup")
		}
	})

	blocked := time.NewTimer(50 * time.Millisecond)
	defer blocked.Stop()
	select {
	case <-finishDone:
		t.Fatalf("FinishRun returned while the earlier flush was held: %v", finishErr)
	case <-blocked.C:
	}
	art.releaseHeldPut()

	finished := time.NewTimer(time.Second)
	defer finished.Stop()
	select {
	case <-finishDone:
	case <-finished.C:
		t.Fatal("FinishRun did not persist after the earlier flush landed")
	}
	if finishErr != nil {
		t.Fatalf("FinishRun: %v", finishErr)
	}

	reader := s3state.New(art)
	t.Cleanup(func() { _ = reader.Close() })
	got, err := reader.GetRun(ctx, "r")
	if err != nil {
		t.Fatalf("FinishRun returned nil but the run does not read back: %v", err)
	}
	if got.Status != "succeeded" {
		t.Errorf("persisted status = %q, want succeeded", got.Status)
	}
	if got.FinishedAt == nil {
		t.Error("persisted run has no FinishedAt")
	}
}
