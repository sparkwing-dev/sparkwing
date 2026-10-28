package controller

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// RunStatusFacts combines one committed run/job snapshot with complete attempt
// history and validated log subjects from that read. Failures carry the masked
// run and job errors, including a job's reported before any attempt started and
// the run's when its pipeline failed to plan. JobResults carry each job's masked
// summary and annotations. Notices carry engine version policy and unclaimed-work
// observations. Private fields stay here until the public display projection.
type RunStatusFacts struct {
	Snapshot   store.RunSnapshot
	Attempts   []runstate.NodeAttempt
	Logs       []blob.LogSubject
	Failures   []store.RunFailure
	JobResults []store.JobResult
	Notices    []engineid.Notice
}

func (s *EngineController) handleRunDetail(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	detail, err := s.RunDetail(r.Context(), a)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, detail)
	return nil
}

// RunDetail projects shared display inputs and public status without exposing
// execution seals. Local, remote and dashboard reads use the same projection.
func (s *EngineController) RunDetail(ctx context.Context, a capability.Authorized) (controllerwire.RunDetail, error) {
	if a.Target.Team == "" || a.Target.RunID == "" {
		return controllerwire.RunDetail{}, routepolicy.MissingEndpointPolicy()
	}
	read := s.ReadRunStatus
	if read == nil {
		if s.ResolveTenant == nil {
			return controllerwire.RunDetail{}, routepolicy.MissingEndpointPolicy()
		}
		read = s.readStoredRunStatus
	}
	facts, err := read(ctx, store.Team(a.Target.Team), a.Target.RunID)
	if err != nil {
		if errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
			return controllerwire.RunDetail{}, &apierr.Error{Code: apierr.NotFound, Message: "team is not registered"}
		}
		return controllerwire.RunDetail{}, engineLookupError(err)
	}
	return runDetailResponse(a.Target, facts)
}

func runDetailResponse(target capability.Target, facts RunStatusFacts) (controllerwire.RunDetail, error) {
	run := facts.Snapshot.Run
	if run.ID != target.RunID || string(run.Team) != target.Team || facts.Snapshot.StoreNow.IsZero() ||
		run.Request.RunID == "" || run.Request.RunID != run.ID && run.Lineage.RetryOf == "" || run.Request.Request.Team != target.Team {
		return controllerwire.RunDetail{}, errors.New("invalid persisted run status")
	}
	detail := controllerwire.RunDetail{
		ID: run.ID, Number: run.Number, Team: string(run.Team), Status: string(run.State), StateSeq: run.StateSeq,
		Inputs: runspec.DisplayRequest(run.Request), Jobs: make([]controllerwire.JobDetail, 0, len(facts.Snapshot.Nodes)),
		Logs:    append([]blob.LogSubject{}, facts.Logs...),
		Notices: append([]engineid.Notice{}, facts.Notices...), StartedAt: run.StartedAt,
		FinishedAt: run.FinishedAt, CancelRequestedAt: run.CancelRequestedAt,
	}
	if run.Spec != nil {
		if run.Spec.Request.RunID != run.Request.RunID || run.Spec.Request.Request.Team != target.Team || run.Spec.Request.Hash != run.Request.Hash {
			return controllerwire.RunDetail{}, errors.New("invalid persisted run seals")
		}
		detail.Inputs = run.Display()
		identity := run.Spec.Resolved.Engine
		detail.Engine = &identity
	}
	errorsByJob := make(map[string]string, len(facts.Failures))
	for _, failure := range facts.Failures {
		if failure.JobID == "" {
			detail.Failure = failure.Error
		} else {
			errorsByJob[failure.JobID] = failure.Error
		}
	}
	results := make(map[string]store.JobResult, len(facts.JobResults))
	for _, result := range facts.JobResults {
		results[result.JobID] = result
	}
	nodes := make(map[string]runstate.NodeRow, len(facts.Snapshot.Nodes))
	for _, row := range facts.Snapshot.Nodes {
		if row.Ref.RunID != runspec.RunID(run.ID) || row.Ref.NodeID == "" {
			return controllerwire.RunDetail{}, errors.New("invalid persisted status job")
		}
		if _, exists := nodes[string(row.Ref.NodeID)]; exists {
			return controllerwire.RunDetail{}, errors.New("duplicate persisted status job")
		}
		nodes[string(row.Ref.NodeID)] = row
		job := snapshotJob(row)
		job.Error = errorsByJob[job.ID]
		job.Summary, job.Annotations = results[job.ID].Summary, results[job.ID].Annotations
		detail.Jobs = append(detail.Jobs, job)
	}
	if err := fillJobDeclarations(detail.Jobs, nodes); err != nil {
		return controllerwire.RunDetail{}, err
	}
	attempts, err := statusAttempts(target, nodes, facts.Attempts)
	if err != nil {
		return controllerwire.RunDetail{}, err
	}
	detail.Attempts = attempts
	knownAttempts := make(map[runspec.AttemptIdentity]bool, len(facts.Attempts))
	for _, attempt := range facts.Attempts {
		knownAttempts[attempt.Identity] = true
	}
	for _, subject := range facts.Logs {
		if subject.Team != target.Team || subject.RunID != run.ID {
			return controllerwire.RunDetail{}, errors.New("invalid persisted status log scope")
		}
		if subject.Attempt != nil {
			if !knownAttempts[*subject.Attempt] {
				return controllerwire.RunDetail{}, errors.New("invalid persisted status log attempt")
			}
		}
		if subject.Unit != nil && subject.Unit.Source != nil && nodes[subject.Unit.Source.NodeID].Ref.NodeID == "" {
			return controllerwire.RunDetail{}, errors.New("invalid persisted status log source")
		}
	}
	detail.Inputs.Lineage = run.Lineage
	if run.Retry != nil {
		detail.Retry = &controllerwire.RetryDetail{Mode: run.Retry.Mode, Reused: slices.Sorted(maps.Keys(run.Retry.Results))}
		if detail.Retry.Reused == nil {
			detail.Retry.Reused = []string{}
		}
	}
	return detail, nil
}

// safety: a need on a job this run never planned would draw an edge to nothing, so only planned jobs pass.
func fillJobDeclarations(jobs []controllerwire.JobDetail, nodes map[string]runstate.NodeRow) error {
	for i := range jobs {
		spec := nodes[jobs[i].ID].Spec
		if len(spec) == 0 {
			continue
		}
		// safety: only the display facts are decoded, so a declaration this projection does not read (env values,
		// closures) can neither fail the run page nor reach it; an undecodable one shows the job without them.
		var declaration jobDisplayDeclaration
		if json.Unmarshal(spec, &declaration) != nil {
			continue
		}
		for _, need := range append(declaration.Needs, declaration.NeedsIfPresent...) {
			if _, exists := nodes[need]; exists && !slices.Contains(jobs[i].Needs, need) {
				jobs[i].Needs = append(jobs[i].Needs, need)
			}
		}
		if _, exists := nodes[declaration.OnFailureOf]; exists {
			jobs[i].OnFailureOf = declaration.OnFailureOf
		}
		jobs[i].Groups, jobs[i].Approval = declaration.Groups, len(declaration.Approval) > 0 && string(declaration.Approval) != "null"
	}
	return nil
}

type jobDisplayDeclaration struct {
	Needs          []string        `json:"deps"`
	NeedsIfPresent []string        `json:"deps_if_present"`
	Groups         []string        `json:"groups"`
	OnFailureOf    string          `json:"on_failure_of"`
	Approval       json.RawMessage `json:"approval"`
}

func statusAttempts(target capability.Target, nodes map[string]runstate.NodeRow, rows []runstate.NodeAttempt) ([]controllerwire.AttemptDetail, error) {
	result := make([]controllerwire.AttemptDetail, 0, len(rows))
	type attemptKey struct {
		job     string
		attempt int64
	}
	seen := make(map[attemptKey]bool, len(rows))
	for _, row := range rows {
		id := row.Identity
		node, exists := nodes[id.NodeID]
		key := attemptKey{id.NodeID, id.Attempt}
		if !exists || id.Team != target.Team || id.RunID != target.RunID || id.Attempt <= 0 || runspec.AttemptOrdinal(id.Attempt) > node.Attempt ||
			id.Generation <= 0 || runspec.ClaimGeneration(id.Generation) > node.Generation || seen[key] {
			return nil, errors.New("invalid persisted status attempt")
		}
		seen[key] = true
		started := row.StartedAt
		result = append(result, controllerwire.AttemptDetail{
			JobID: id.NodeID, Attempt: id.Attempt, Generation: id.Generation,
			Outcome: string(row.Outcome), Reason: row.FailureReason, Executor: row.ExecutorKind,
			ExecutorName: row.ExecutorName, Location: row.ExecutorLocation,
			StartedAt: &started, FinishedAt: row.FinishedAt,
		})
	}
	return result, nil
}

func (s *EngineController) readStoredRunStatus(ctx context.Context, team store.Team, run string) (RunStatusFacts, error) {
	tenant, err := s.tenantForTeam(ctx, team)
	if err != nil {
		return RunStatusFacts{}, err
	}
	view, err := tenant.ReadRunView(ctx, run)
	if err != nil {
		return RunStatusFacts{}, err
	}
	return s.runViewStatus(ctx, view)
}

func (s *EngineController) runViewStatus(ctx context.Context, view store.RunView) (RunStatusFacts, error) {
	facts := RunStatusFacts{Snapshot: view.Snapshot, Attempts: view.Attempts, Logs: view.Logs, Failures: view.Failures, JobResults: view.JobResults}
	if view.Snapshot.Run.Spec != nil {
		if s.VersionPolicy == nil {
			return RunStatusFacts{}, versionPolicyUnavailable()
		}
		notices, err := s.VersionPolicy.notices(ctx, view.Snapshot.Run.Spec.Resolved.Engine.Version)
		if err != nil {
			return RunStatusFacts{}, err
		}
		facts.Notices = notices
	}
	if unclaimed := unclaimedNotice(view); unclaimed != nil {
		facts.Notices = append(facts.Notices, *unclaimed)
	}
	return facts, nil
}
