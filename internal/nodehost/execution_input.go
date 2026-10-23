package nodehost

import (
	"encoding/json"
	"reflect"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

// NewNodeInput constructs execution from an authenticated row, selected engine and granted runner.
func NewNodeInput(accepted AcceptedRun, row runstate.NodeRow, selected engine.Resolved, granted nodeproto.RunnerIdentity) (NodeInput, error) {
	var spec runspec.Spec
	if accepted.RunID == "" || accepted.SpecHash == "" || accepted.PlanHash == "" || json.Unmarshal(accepted.Spec, &spec) != nil || string(spec.Hash) != accepted.SpecHash || string(spec.PlanHash) != accepted.PlanHash {
		return NodeInput{}, executorError(apierr.SpecDrift, "execution requires the accepted run seal")
	}
	if selected.Identity.Version == "" || selected.Identity.ManifestDigest == "" || selected.Identity.Pin.SDK == "" || selected.Identity.Pin.Version == "" || selected.Identity != spec.Resolved.Engine {
		return NodeInput{}, executorError(apierr.SpecDrift, "execution engine differs from the accepted release")
	}
	if granted.Name == "" || granted.Type == "" || granted.Labels == nil {
		return NodeInput{}, executorError(apierr.SpecDrift, "execution requires the granted runner identity")
	}
	if row.Ref.RunID != runspec.RunID(accepted.RunID) || row.Ref.NodeID == "" || row.ClaimedBy == "" || row.CapabilityPrefix == "" || row.Generation < 1 || row.Attempt < 0 {
		return NodeInput{}, executorError(apierr.LockHeld, "execution requires a persisted job award")
	}
	if row.Status != runstate.StatusPending || row.Outcome != "" {
		return NodeInput{}, executorError(apierr.IllegalTransition, "execution requires a claimed, unstarted job")
	}
	if row.SpecHash == "" {
		return NodeInput{}, executorError(apierr.SpecDrift, "execution requires the persisted job hash")
	}
	if execution, ok := spec.Resolved.Settings.Jobs[string(row.Ref.NodeID)]; ok && !reflect.DeepEqual(execution, row.Execution) {
		return NodeInput{}, executorError(apierr.SpecDrift, "job settings differ from the accepted run")
	}
	granted.Labels = slices.Clone(granted.Labels)
	return NodeInput{
		Accepted: accepted, NodeID: string(row.Ref.NodeID), Engine: selected, GrantedRunner: granted,
		Generation: row.Generation, Attempt: row.Attempt, SpecHash: row.SpecHash, Execution: row.Execution,
		Authority:    ClaimAuthority{Holder: row.ClaimedBy, CapabilityPrefix: row.CapabilityPrefix},
		RouteBinding: nodeproto.ProcessBinding{RunID: accepted.RunID, JobID: string(row.Ref.NodeID)},
	}, nil
}
