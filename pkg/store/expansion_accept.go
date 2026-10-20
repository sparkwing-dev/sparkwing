package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

type expansionAcceptance struct {
	selection     secretSelection
	state         ExpansionState
	hash          string
	body, failure []byte
	plan          nodeproto.PlanDoc
}

func prepareExpansionTx(ctx context.Context, tx *storeTx, team Team, request ExpansionSubmission, snapshot RunSnapshot) (expansionAcceptance, error) {
	var result expansionAcceptance
	if request.Failure != nil {
		result.state = ExpansionFailed
		var err error
		result.failure, err = json.Marshal(request.Failure)
		if err != nil {
			return result, err
		}
		result.hash, err = stateHash(request.Failure)
		return result, err
	}
	priorJobs := map[string]bool{}
	for _, existing := range snapshot.Expansions {
		if existing.Source != request.Source {
			continue
		}
		var plan nodeproto.PlanDoc
		if err := json.Unmarshal(existing.Body, &plan); err != nil {
			return result, err
		}
		for _, job := range plan.Jobs {
			priorJobs[job.ID] = true
		}
	}
	selection, err := runSecretSelection(snapshot.Run.Request)
	if err != nil {
		return result, err
	}
	result.selection = selection
	secrets, err := planSecretsTx(ctx, tx, team, *snapshot.Run.Spec, selection)
	if err != nil {
		return result, err
	}
	var plan nodeproto.PlanDoc
	if err := decodeScheduleDeclaration(snapshot.Run.Plan, &plan); err != nil {
		return result, err
	}
	validation := nodeproto.ValidationContext{Placement: RequestPlacement(snapshot.Run.Request.Request), Secrets: secrets, Expansion: true, SourceJob: request.Source.NodeID, ExistingConcurrency: plan.Concurrency}
	for _, node := range snapshot.Nodes {
		if priorJobs[node.Ref.NodeID] {
			continue
		}
		var job nodeproto.JobSpec
		if err := json.Unmarshal(node.Spec, &job); err != nil {
			return result, err
		}
		validation.ExistingJobs = append(validation.ExistingJobs, job)
	}
	result.plan, err = tx.store.planServices().ValidatePlan(request.Body, validation)
	if err != nil {
		return result, err
	}
	if result.plan.Jobs == nil {
		return result, authorityError(apierr.PlanInvalid, "expansion requires an explicit jobs array")
	}
	result.body, err = json.Marshal(struct {
		Jobs []nodeproto.JobSpec `json:"jobs"`
	}{result.plan.Jobs})
	if err != nil {
		return result, err
	}
	hash, err := tx.store.planServices().CanonicalHash(json.RawMessage(result.body))
	result.hash, result.state = string(hash), ExpansionAccepted
	return result, err
}

func acceptedExpansionNodes(snapshot RunSnapshot, plan nodeproto.PlanDoc) ([]NodeRow, error) {
	var result []NodeRow
	for _, job := range plan.Jobs {
		index := slices.IndexFunc(snapshot.Nodes, func(node NodeRow) bool { return node.Ref.NodeID == job.ID })
		if index < 0 {
			return nil, errors.New("accepted expansion job is missing")
		}
		result = append(result, snapshot.Nodes[index])
	}
	return result, nil
}

func insertExpansionTx(ctx context.Context, tx *storeTx, team Team, request ExpansionSubmission, snapshot RunSnapshot, accepted expansionAcceptance) ([]NodeRow, error) {
	executionSettings, err := tx.store.planServices().ResolveExpansion(settings.ExpansionInputs{Spec: *snapshot.Run.Spec, Plan: accepted.plan, Context: settings.Context{Local: RequestPlacement(snapshot.Run.Request.Request) == nodeproto.PlacementLocal}})
	if err != nil {
		return nil, err
	}
	if len(executionSettings) != len(accepted.plan.Jobs) {
		return nil, errors.New("resolved execution settings do not match expansion jobs")
	}
	if err := checkExpansionSecretPoolsTx(ctx, tx, team, *snapshot.Run.Spec, executionSettings, accepted.selection); err != nil {
		return nil, err
	}
	var nodes []NodeRow
	for _, job := range accepted.plan.Jobs {
		execution, exists := executionSettings[job.ID]
		if !exists {
			return nil, errors.New("expanded job has no resolved execution settings")
		}
		spec, err := json.Marshal(job)
		if err != nil {
			return nil, err
		}
		hash, err := tx.store.planServices().CanonicalHash(job)
		if err != nil {
			return nil, err
		}
		expansion := ExpansionAbsent
		if len(job.Closures.Generate) > 0 {
			expansion = ExpansionExpected
		}
		node := NodeRow{Ref: NodeRef{RunID: request.Source.RunID, NodeID: job.ID}, Status: StatusPending, Spec: spec, SpecHash: string(hash), Execution: execution, Expansion: expansion}
		if err := insertNodeTx(ctx, tx, team, node); err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	snapshot.Nodes = append(snapshot.Nodes, nodes...)
	for i := range snapshot.Nodes {
		if snapshot.Nodes[i].Ref == request.Source {
			snapshot.Nodes[i].Expansion, snapshot.Nodes[i].ExpansionDigest = ExpansionAccepted, accepted.hash
		}
	}
	snapshot.Expansions = append(snapshot.Expansions, AcceptedExpansionRow{Source: request.Source, SourceGeneration: request.SourceGeneration, Body: accepted.body, BodyHash: accepted.hash})
	if _, err := snapshot.Scheduling(); err != nil {
		return nil, err
	}
	return nodes, nil
}

func checkExpansionSecretPoolsTx(ctx context.Context, tx *storeTx, team Team, spec runspec.Spec, jobs map[string]runspec.JobExecutionSettings, selection secretSelection) error {
	for _, name := range spec.Resolved.SecretNames {
		if slices.Contains(spec.Request.Request.LocalSecretNames, name) {
			continue
		}
		_, pool, err := selectedSecretTx(ctx, tx, team, name, selection)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if pool == "" {
			continue
		}
		if spec.Request.Request.Options.Runner.Value.Kind == runspec.RunnerHere || !slices.Contains(spec.Request.AllowedPools, pool) {
			return authorityError(apierr.Forbidden, "restricted secret requires an authorized pool")
		}
		for _, job := range jobs {
			if !slices.Contains(job.Requires.Value, "pool="+pool) {
				return authorityError(apierr.PlanInvalid, "restricted secret requires its pool on every generated job")
			}
		}
	}
	return nil
}
