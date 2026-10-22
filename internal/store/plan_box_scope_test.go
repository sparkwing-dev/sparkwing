package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestAcceptPlanRefusesRunBoxBeforePersistence(t *testing.T) {
	for _, dispatch := range []runspec.Dispatch{runspec.DispatchHost, runspec.DispatchController} {
		t.Run(string(dispatch), func(t *testing.T) {
			tenant, request, _ := planFixture(t, dispatch)
			request.Seal.Plan = []byte(`{"jobs":[{"id":"job","steps":[{"id":"body"}]}],"concurrency":[{"group":"deploys","scope":"box"}]}`)
			_, err := tenant.AcceptPlan(t.Context(), request)
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.PlanInvalid || !strings.Contains(err.Error(), "deploys") || !strings.Contains(err.Error(), "job-level box scope") {
				t.Fatalf("acceptance lost group and alternative: %v", err)
			}
			run, err := tenant.ReadRun(t.Context(), request.RunID)
			if err != nil || run.Spec != nil {
				t.Fatalf("rejected plan was sealed: %+v, %v", run.Spec, err)
			}
			if _, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "job"}); !errors.Is(err, ErrNotFound) {
				t.Fatalf("rejected plan persisted job: %v", err)
			}
		})
	}
}
