package controller

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/receipt"
)

func (s *Server) handleGetRunReceipt(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	tenant, err := s.reportingTenant(r, authorized)
	if err != nil {
		return engineLookupError(err)
	}
	runID := authorized.Target.RunID
	snapshot, err := tenant.ReadSnapshot(r.Context(), runID)
	if err != nil {
		return engineLookupError(err)
	}
	run, nodes, err := receiptSnapshot(snapshot)
	if err != nil {
		return engineLookupError(err)
	}
	rec := receipt.BuildReceipt(run, nodes, s.costPerRunnerHour, s.costRateSource)
	writeJSON(w, http.StatusOK, rec)
	return nil
}

func receiptSnapshot(snapshot store.RunSnapshot) (*store.Run, []*store.Node, error) {
	row := snapshot.Run
	display := row.Display()
	args := display.RequestedArgs
	if row.Spec != nil {
		args = display.Args
	}
	secrets := display.SecretArgs
	if secrets == nil {
		for name := range args {
			secrets = append(secrets, name)
		}
	}
	run := &store.Run{
		ID: row.ID, Pipeline: row.Pipeline, Status: string(row.State), GitSHA: display.Git.SHA,
		StartedAt: row.CreatedAt, FinishedAt: row.FinishedAt, Args: args, PlanSnapshot: row.Plan,
		Invocation: map[string]any{store.InvocationSecretArgsKey: secrets},
	}
	if row.StartedAt != nil {
		run.StartedAt = *row.StartedAt
	}
	nodes := make([]*store.Node, 0, len(snapshot.Nodes))
	for _, row := range snapshot.Nodes {
		var declaration nodeproto.JobSpec
		if err := json.Unmarshal(row.Spec, &declaration); err != nil || runspec.NodeID(declaration.ID) != row.Ref.NodeID {
			return nil, nil, errors.New("invalid persisted receipt job")
		}
		nodes = append(nodes, &store.Node{
			RunID: string(row.Ref.RunID), NodeID: string(row.Ref.NodeID),
			Status: string(row.Status), Outcome: string(row.Outcome), StatusDetail: row.StatusDetail,
			StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Deps: declaration.Needs,
		})
	}
	return run, nodes, nil
}
