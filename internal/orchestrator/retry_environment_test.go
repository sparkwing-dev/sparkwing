package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestConsumerRefusesUncapturedRetriesBeforeDispatch(t *testing.T) {
	original := dispatchLocalTriggerFn
	t.Cleanup(func() { dispatchLocalTriggerFn = original })
	dispatchLocalTriggerFn = func(context.Context, *store.Trigger, string, string, *localCompileCache, *slog.Logger, []string) error {
		t.Error("retry reached dispatch without its execution environment")
		return nil
	}
	for _, source := range []string{store.RetrySourceManual, store.RetrySourceAuto} {
		t.Run(source, func(t *testing.T) {
			home := t.TempDir()
			st := consumerTestStore(t, home)
			if err := st.CreateTriggerWithRun(t.Context(), store.Trigger{
				ID: "retry", Pipeline: "build", RetryOf: "source", RetrySource: source, CreatedAt: time.Now(),
			}, store.Run{ID: "retry", Pipeline: "build", Status: "pending", StartedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			trigger, err := st.ClaimNextTrigger(t.Context(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			runClaimedTrigger(t.Context(), st, trigger, nil, quietLogger(), home, time.Minute)
			run, err := st.GetRun(t.Context(), trigger.ID)
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != "failed" || !strings.Contains(run.Error, "retry execution environment is unavailable") {
				t.Fatalf("retry result=%s: %s", run.Status, run.Error)
			}
		})
	}
}
