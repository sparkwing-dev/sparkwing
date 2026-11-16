package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
)

func TestReviewForeignCancellationBeforeStop(t *testing.T) {
	for _, phase := range []string{"watch", "drain"} {
		t.Run(phase, func(t *testing.T) {
			ready, release := make(chan struct{}), make(chan struct{})
			foreign, cancelForeign := context.WithCancel(t.Context())
			cancelForeign()
			foreignErr := foreign.Err()
			var ownerWasLive atomic.Bool
			result := func(ctx context.Context) error {
				ownerWasLive.Store(ctx.Err() == nil)
				close(ready)
				<-ctx.Done()
				return foreignErr
			}
			lifecycle := &queueLifecycleFixture{}
			if phase == "watch" {
				lifecycle.watch = func(ctx context.Context) (localhostwire.LocalRunnerInstance, error) {
					return localhostwire.LocalRunnerInstance{}, result(ctx)
				}
			} else {
				lifecycle.drain = result
			}
			loop := &runners.Loop{
				Config:       runners.LoopConfig{Version: "development", MaxConcurrent: 1, MaxClaims: 1},
				CheckVersion: func(context.Context, string) error { return nil },
				Host:         queueHostFunc(func(context.Context, controllerwire.ClaimedUnit) error { <-release; return nil }),
			}
			done := make(chan error, 1)
			go func() { done <- runLocalQueue(t.Context(), "review", localQueueTestClient(t, nil), loop, lifecycle) }()
			<-ready
			close(release)
			got := <-done
			if !ownerWasLive.Load() {
				t.Fatal("fixture did not establish live owner")
			}
			if !errors.Is(got, foreignErr) {
				t.Fatalf("foreign cancellation before owner stop was lost: %v", got)
			}
		})
	}
}
