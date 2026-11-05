package runners

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestLoopHonorsIdlePollAdvice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		start, calls := time.Now(), 0
		loop := &Loop{
			Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, PollInterval: time.Second}, CheckVersion: func(context.Context, string) error { return nil },
			Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				calls++
				if calls == 2 {
					if elapsed := time.Since(start); elapsed != 5*time.Second {
						t.Errorf("poll after %s, want 5s", elapsed)
					}
					cancel()
				}
				return controllerwire.ClaimResponse{PollAfter: 5 * time.Second}, nil
			}), Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { t.Error("unexpected work"); return nil }),
		}
		if err := loop.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}
