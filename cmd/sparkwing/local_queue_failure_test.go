package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
)

func TestLocalRunnerWatchKeepsFailureWhenCancelled(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprint(mixed), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			failure := errors.New("lifecycle watch refused")
			lifecycle := &queueLifecycleFixture{watch: func(context.Context) (localhostwire.LocalRunnerInstance, error) {
				cancel()
				err := failure
				if mixed {
					err = errors.Join(context.Canceled, failure)
				}
				return localhostwire.LocalRunnerInstance{}, err
			}}
			defer cancel()
			if err := watchLocalRunner(ctx, lifecycle, "fixture", &runners.Loop{}); !errors.Is(err, failure) {
				t.Fatalf("cancelled watch lost its failure: %v", err)
			}
		})
	}
}

func TestLocalQueueCleanupPreservesFailure(t *testing.T) {
	for _, phase := range []string{"watch", "drain"} {
		for _, mixed := range []bool{false, true} {
			t.Run(phase+fmt.Sprint(mixed), func(t *testing.T) {
				failure := errors.New("lifecycle cleanup failed")
				result := func(ctx context.Context) error {
					<-ctx.Done()
					if mixed {
						return errors.Join(fmt.Errorf("request: %w", ctx.Err()), failure)
					}
					return fmt.Errorf("request: %w", ctx.Err())
				}
				stopped, revoked := false, false
				lifecycle := &queueLifecycleFixture{stop: func() { stopped = true }}
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
					Host:         queueHostFunc(func(context.Context, controllerwire.ClaimedUnit) error { return nil }),
				}
				options := localQueueTestClient(t, func(context.Context) { revoked = true })
				err := runLocalQueue(t.Context(), "fixture", options, loop, lifecycle)
				if errors.Is(err, failure) != mixed || (!mixed && err != nil) || !stopped || !revoked {
					t.Fatalf("cleanup error=%v stopped=%t revoked=%t", err, stopped, revoked)
				}
			})
		}
	}
}

func TestLocalQueueKeepsCancellationBeforeOwnerStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, returned, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
		lifecycle := &queueLifecycleFixture{watch: func(context.Context) (localhostwire.LocalRunnerInstance, error) {
			<-started
			close(returned)
			return localhostwire.LocalRunnerInstance{}, context.Canceled
		}}
		loop := &runners.Loop{
			Config:       runners.LoopConfig{Version: "development", MaxConcurrent: 1, MaxClaims: 1},
			CheckVersion: func(context.Context, string) error { return nil },
			Host: queueHostFunc(func(context.Context, controllerwire.ClaimedUnit) error {
				close(started)
				<-release
				return nil
			}),
		}
		done := make(chan error, 1)
		go func() { done <- runLocalQueue(t.Context(), "fixture", localQueueTestClient(t, nil), loop, lifecycle) }()
		<-returned
		// safety: finish the unrelated cancellation before cleanup cancels the watch owner.
		synctest.Wait()
		close(release)
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("unrelated cancellation became clean owner stop: %v", err)
		}
	})
}
