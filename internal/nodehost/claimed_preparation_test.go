package nodehost

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestClaimedSourcePreparationReachesAttemptLog(t *testing.T) {
	var receipts []source.Timing
	var wantAttempt runspec.AttemptOrdinal
	input, _, host, _ := claimedNodeFixture(t, func(_ *NodeInput, deps *ExecutorDependencies) {
		deps.Log = func(_ context.Context, got NodeInput, frame nodewire.FrameLog) error {
			if frame.Event != "preparation" {
				return nil
			}
			if got.NodeID != "build" || got.Generation < 1 || got.Attempt != wantAttempt {
				t.Fatalf("lost attempt identity: %s/%d/%d", got.NodeID, got.Generation, got.Attempt)
			}
			var receipt source.Timing
			if err := json.Unmarshal(frame.Attrs, &receipt); err != nil {
				t.Fatal(err)
			}
			receipts = append(receipts, receipt)
			return nil
		}
	})
	wantAttempt = input.Attempt + 1
	acquire := host.Acquire
	host.Acquire = func(ctx context.Context, spec source.Spec, options source.Options) (*source.Entry, error) {
		options.RecordTiming(ctx, "checkout", time.Now(), true, false)
		return acquire(ctx, spec, options)
	}
	if err := host.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || receipts[0].Stage != "checkout" || !receipts[0].Success {
		t.Fatalf("claimed preparation disappeared: %+v", receipts)
	}
}

func TestClaimedSourcePreparationMasksSecretTimingFields(t *testing.T) {
	var frames []nodewire.FrameLog
	input, _, host, _ := claimedNodeFixture(t, func(_ *NodeInput, deps *ExecutorDependencies) {
		deps.Log = func(_ context.Context, _ NodeInput, frame nodewire.FrameLog) error {
			if frame.Event == "preparation" {
				frames = append(frames, frame)
			}
			return nil
		}
	})
	acquire := host.Acquire
	host.Acquire = func(ctx context.Context, spec source.Spec, options source.Options) (*source.Entry, error) {
		options.RecordTiming(ctx, "checkout-private", time.Now(), true, false)
		return acquire(ctx, spec, options)
	}
	if err := host.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 {
		t.Fatalf("preparation frames=%d, want one", len(frames))
	}
	if attrs := string(frames[0].Attrs); strings.Contains(attrs, "private") || !strings.Contains(attrs, "***") {
		t.Fatalf("claimed preparation frame escaped masking: %s", attrs)
	}
}
