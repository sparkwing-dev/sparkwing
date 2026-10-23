package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

type expansionAcceptance struct {
	selection     secretSelection
	state         runstate.ExpansionState
	hash          string
	bodyHash      string
	body, failure []byte
	registration  []byte
	plan          nodeproto.PlanDoc
}

func prepareExpansionTx(ctx context.Context, tx *storeTx, team Team, request ExpansionSubmission, snapshot RunSnapshot) (expansionAcceptance, error) {
	var result expansionAcceptance
	if request.Failure != nil {
		if len(request.Registration.Defaults) > 0 || len(request.Registration.ListArgs) > 0 || len(request.Registration.Values) > 0 {
			return result, authorityError(apierr.BadRequest, "failed expansion cannot register secrets")
		}
		result.state = runstate.ExpansionFailed
		var err error
		result.failure, err = json.Marshal(request.Failure)
		if err != nil {
			return result, err
		}
		result.hash, err = stateHash(request.Failure)
		return result, err
	}
	if request.Registration.Version != 1 {
		return result, authorityError(apierr.PlanInvalid, "expansion requires an explicit version 1 secret registration")
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
	registration, err := runspec.BuildSecretRegistration(runspec.Request{}, runspec.Resolved{SecretRegistration: request.Registration})
	if err != nil {
		return result, authorityError(apierr.PlanInvalid, err.Error())
	}
	if len(request.Registration.Values) > 0 && !slices.Equal(request.Registration.Values, registration.Values) {
		return result, authorityError(apierr.SpecDrift, "expansion secret registration differs from declarations")
	}
	for _, value := range registration.Values {
		if err := secrets.Register(value); err != nil {
			return result, authorityError(apierr.PlanInvalid, err.Error())
		}
	}
	result.registration, err = json.Marshal(registration)
	if err != nil {
		return result, err
	}
	var plan nodeproto.PlanDoc
	if err := decodeScheduleDeclaration(snapshot.Run.Plan, &plan); err != nil {
		return result, err
	}
	validation := nodeproto.ValidationContext{Placement: RequestPlacement(snapshot.Run.Request.Request), Secrets: secrets, Expansion: true, SourceJob: string(request.Source.NodeID), ExistingConcurrency: plan.Concurrency}
	for _, node := range snapshot.Nodes {
		if priorJobs[string(node.Ref.NodeID)] {
			continue
		}
		var job nodeproto.JobSpec
		if err := json.Unmarshal(node.Spec, &job); err != nil {
			return result, err
		}
		validation.ExistingJobs = append(validation.ExistingJobs, job)
	}
	result.plan, err = validatePlan(request.Body, validation)
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
	bodyHash, err := runspec.CanonicalHash(json.RawMessage(result.body))
	if err != nil {
		return result, err
	}
	result.bodyHash = string(bodyHash)
	hash, err := runspec.CanonicalHash(struct {
		Body         json.RawMessage
		Registration runspec.SecretRegistration
	}{result.body, registration})
	result.hash, result.state = string(hash), runstate.ExpansionAccepted
	return result, err
}

func acceptedExpansionNodes(snapshot RunSnapshot, plan nodeproto.PlanDoc) ([]runstate.NodeRow, error) {
	var result []runstate.NodeRow
	for _, job := range plan.Jobs {
		index := slices.IndexFunc(snapshot.Nodes, func(node runstate.NodeRow) bool { return node.Ref.NodeID == runspec.NodeID(job.ID) })
		if index < 0 {
			return nil, errors.New("accepted expansion job is missing")
		}
		result = append(result, snapshot.Nodes[index])
	}
	return result, nil
}

func insertExpansionTx(ctx context.Context, tx *storeTx, team Team, request ExpansionSubmission, snapshot RunSnapshot, accepted expansionAcceptance) ([]runstate.NodeRow, error) {
	executionSettings, err := settings.ResolveExpansion(settings.ExpansionInputs{Spec: *snapshot.Run.Spec, Plan: accepted.plan, Context: settings.Context{Local: RequestPlacement(snapshot.Run.Request.Request) == nodeproto.PlacementLocal}})
	if err != nil {
		return nil, err
	}
	if len(executionSettings) != len(accepted.plan.Jobs) {
		return nil, errors.New("resolved execution settings do not match expansion jobs")
	}
	if err := checkExpansionSecretPoolsTx(ctx, tx, team, *snapshot.Run.Spec, executionSettings, accepted.selection); err != nil {
		return nil, err
	}
	var nodes []runstate.NodeRow
	for _, job := range accepted.plan.Jobs {
		execution, exists := executionSettings[job.ID]
		if !exists {
			return nil, errors.New("expanded job has no resolved execution settings")
		}
		spec, err := json.Marshal(job)
		if err != nil {
			return nil, err
		}
		hash, err := runspec.CanonicalHash(job)
		if err != nil {
			return nil, err
		}
		expansion := runstate.ExpansionAbsent
		if len(job.Closures.Generate) > 0 {
			expansion = runstate.ExpansionExpected
		}
		node := runstate.NodeRow{Ref: runspec.NodeRef{RunID: request.Source.RunID, NodeID: runspec.NodeID(job.ID)}, Status: runstate.StatusPending, Spec: spec, SpecHash: string(hash), Execution: execution, Expansion: expansion}
		if err := insertNodeTx(ctx, tx, team, node); err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	snapshot.Nodes = append(snapshot.Nodes, nodes...)
	for i := range snapshot.Nodes {
		if snapshot.Nodes[i].Ref == request.Source {
			snapshot.Nodes[i].Expansion, snapshot.Nodes[i].ExpansionDigest = runstate.ExpansionAccepted, accepted.hash
		}
	}
	var registration runspec.SecretRegistration
	if err := json.Unmarshal(accepted.registration, &registration); err != nil {
		return nil, err
	}
	snapshot.Expansions = append(snapshot.Expansions, runstate.AcceptedExpansionRow{Source: request.Source, SourceGeneration: int64(request.SourceGeneration), Body: accepted.body, BodyHash: accepted.bodyHash, Registration: registration})
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
