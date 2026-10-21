//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

const splitSecret = "hunter2-straddles-the-split"

func outerLayerFailure() error {
	return errors.New(strings.Repeat("h", int(limits.ErrorBytes)/2-20) + splitSecret + " slot full\n" + strings.Repeat("y", 2*int(limits.ErrorBytes)))
}

func checkMaskedNativeFailure(t *testing.T, f *storeHostFixture, input NodeInput, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("failure was lost")
	}
	row, readErr := f.Tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: runspec.RunID(input.Accepted.RunID), NodeID: runspec.NodeID(input.NodeID)})
	if readErr != nil {
		t.Fatal(readErr)
	}
	var storedError string
	if err := teststore.SQL(t, f.Store).QueryRowContext(t.Context(), "SELECT error FROM nodes WHERE team=? AND run_id=? AND node_id=?", store.DefaultTeam, input.Accepted.RunID, input.NodeID).Scan(&storedError); err != nil {
		t.Fatal(err)
	}
	events, readErr := f.Tenant.ListEventsAfter(t.Context(), input.Accepted.RunID, 0, 100)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var payloads strings.Builder
	for _, event := range events {
		if event.Kind == "job_failed" {
			payloads.Write(event.Payload)
		}
	}
	if row.Outcome != "failed" || payloads.Len() == 0 {
		t.Fatalf("row = %+v, events = %+v", row, events)
	}
	for name, text := range map[string]string{"row": storedError, "events": payloads.String(), "error": err.Error()} {
		if strings.Contains(text, splitSecret[:10]) {
			t.Errorf("%s holds a secret fragment", name)
		}
	}
	if len(storedError) > int(limits.ErrorBytes) || !strings.Contains(storedError, "***") || len(err.Error()) > int(limits.ErrorBytes) {
		t.Fatalf("unbounded or unmasked failure: row=%d error=%d", len(storedError), len(err.Error()))
	}
}

func maskingExecutorFixture(t *testing.T) (*storeHostFixture, NodeInput, ExecutorDependencies) {
	t.Helper()
	f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "build", ResultStep: "body", Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
	releaseRemote(t, f, "build")
	_, input := claimRemote(t, f, "build")
	entry, err := f.Host.Acquire(t.Context(), f.Input)
	if err != nil {
		t.Fatal(err)
	}
	input.Entry = entry
	_, _, deps, _ := executorFixture(t)
	deps.Store = f.State
	masker := secrets.NewMasker()
	masker.Register(splitSecret)
	deps.Mask = masker.Mask
	deps.EnsureEngine = func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
		return input.Engine, nil
	}
	deps.Start = func(context.Context, NodeInput, ExecutorActivity) (ExecutorProcess, error) {
		return &executorProcess{plan: f.Accepted.Plan, result: nodeproto.RunResult{Outcome: "success", Output: json.RawMessage(`{"v":1}`)}}, nil
	}
	return f, input, deps
}

func TestOuterLayerNodeFailuresAreMaskedThenBounded(t *testing.T) {
	f, input, deps := maskingExecutorFixture(t)
	setTestExecutor(&deps, func(context.Context, testPolicyName, NodeInput, nodeproto.JobSpec) (ExecutorFinalizer, error) {
		return nil, outerLayerFailure()
	})
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	checkMaskedNativeFailure(t, f, input, host.Execute(t.Context(), input))
}

func TestARefusedOutputsErrorIsMaskedThenBounded(t *testing.T) {
	f, input, deps := maskingExecutorFixture(t)
	deps.PublishOutput = func(context.Context, NodeInput, json.RawMessage) (*blob.RecordKey, error) {
		lead := strings.Repeat("h", int(limits.ErrorBytes)/2-20-len(blob.ErrNotFound.Error()+": "))
		return nil, fmt.Errorf("%w: %s", blob.ErrNotFound, lead+splitSecret+"\n"+strings.Repeat("y", 2*int(limits.ErrorBytes)))
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	checkMaskedNativeFailure(t, f, input, host.Execute(t.Context(), input))
}
