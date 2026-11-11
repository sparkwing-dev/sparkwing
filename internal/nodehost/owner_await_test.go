package nodehost

import (
	"context"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestOwnerHostAwaitingDescendantsReleasesRunnerSlot(t *testing.T) {
	slots := make(chan struct{}, 2)
	var run func(context.Context, int) error
	run = func(parent context.Context, depth int) error {
		select {
		case slots <- struct{}{}:
		default:
			return fmt.Errorf("descendant at depth %d cannot plan: both owner slots are held", depth)
		}
		held := true
		ctx := WithAwaitSlot(parent, func() func(context.Context) {
			<-slots
			held = false
			return func(context.Context) {
				slots <- struct{}{}
				held = true
			}
		})
		defer func() {
			if held {
				<-slots
			}
		}()
		_, host, input := newOwnerFixture(t, true)
		finished := depth == 3
		host.Decide = func(store.ScheduleSnapshot) store.ScheduleDecision {
			if finished {
				return store.ScheduleDecision{Outcome: "success"}
			}
			return store.ScheduleDecision{}
		}
		host.Wait = func(ctx context.Context, _ store.RunSnapshot) error {
			if err := run(ctx, depth+1); err != nil {
				return err
			}
			finished = true
			return nil
		}
		completion, err := host.Run(ctx, input)
		if err == nil && completion.Outcome != "success" {
			return fmt.Errorf("owner %d ended %s", depth, completion.Outcome)
		}
		return err
	}
	if err := run(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	if len(slots) != 0 {
		t.Fatalf("finished owners retained %d slots", len(slots))
	}
}
