package nodehost

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestNodeExecutorExplainsExhaustedApplicationAttempts(t *testing.T) {
	for _, tc := range []struct{ retries, previous, total int }{{0, 0, 1}, {1, 0, 2}, {2, 1, 3}} {
		t.Run(fmt.Sprintf("retries=%d/previous=%d", tc.retries, tc.previous), func(t *testing.T) {
			input, state, deps, processes := executorFixture(t)
			executorRetryBudget(t, &input, state, tc.retries)
			state.row.ApplicationRetries = tc.previous
			start := deps.Start
			deps.Start = func(ctx context.Context, in NodeInput, progress ExecutorActivity) (ExecutorProcess, error) {
				process, err := start(ctx, in, progress)
				process.(*executorProcess).result = nodeproto.RunResult{Outcome: "failed", Error: &apierr.Error{Code: apierr.SDKError, Message: fmt.Sprintf("application attempt %d failed: private", tc.previous+len(*processes))}}
				return process, err
			}
			host, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			if err := host.Execute(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			report := state.changes[len(state.changes)-1].Extra.Result
			if report == nil || report.Failure == nil || report.Failure.Error == nil {
				t.Fatalf("missing final failure: %+v", report)
			}
			if !strings.Contains(report.MaskedError, fmt.Sprintf("application attempt %d failed", tc.total)) || strings.Contains(report.MaskedError, "private") || report.Failure.Error.Message != report.MaskedError {
				t.Fatalf("last masked error was lost: %+v", report)
			}
			if tc.retries == 0 {
				if strings.Contains(report.MaskedError, "all ") {
					t.Fatalf("non-retried failure claims exhaustion: %s", report.MaskedError)
				}
				return
			}
			for _, want := range []string{fmt.Sprintf("all %d attempts failed", tc.total), "fix the last error and retry the run"} {
				if !strings.Contains(report.MaskedError, want) {
					t.Errorf("final error %q omits %q", report.MaskedError, want)
				}
			}
		})
	}
}
