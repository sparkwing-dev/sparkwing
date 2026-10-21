package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (h *sdkCode) recordedReplay(ctx context.Context, input NodeInput, initial []nodeproto.JobSpec) ([]GeneratorReplay, error) {
	if h.ReadOutput == nil && (input.Storage.Bytes == nil || input.Storage.Records == nil) {
		return nil, executorError(apierr.PlanDrift, "generated reconstruction requires authorized ancestor outputs")
	}
	snapshot, err := h.Store.ReadSnapshot(ctx, input.Accepted.RunID)
	if err != nil {
		return nil, err
	}
	if snapshot.Run.ID != input.Accepted.RunID {
		return nil, executorError(apierr.PlanDrift, "generator ancestry belongs to another run")
	}
	declarations := map[string]nodeproto.JobSpec{}
	for _, job := range initial {
		declarations[job.ID] = job
	}
	parents := map[string]store.AcceptedExpansionRow{}
	for _, expansion := range snapshot.Expansions {
		var plan nodeproto.PlanDoc
		if expansion.Source.RunID != runspec.RunID(input.Accepted.RunID) || expansion.Source.NodeID == "" || expansion.SourceGeneration < 0 || json.Unmarshal(expansion.Body, &plan) != nil || plan.Jobs == nil {
			return nil, executorError(apierr.PlanDrift, "generator ancestry has an invalid accepted expansion")
		}
		hash, err := runspec.CanonicalHash(expansion.Body)
		if err != nil {
			return nil, err
		}
		if string(hash) != expansion.BodyHash {
			return nil, executorError(apierr.PlanDrift, "generator ancestry expansion hash differs from its body")
		}
		for _, job := range plan.Jobs {
			if _, exists := declarations[job.ID]; exists || job.ID == "" {
				return nil, executorError(apierr.PlanDrift, "generator ancestry has ambiguous job membership")
			}
			declarations[job.ID], parents[job.ID] = job, expansion
		}
	}
	var replay []GeneratorReplay
	visiting := map[string]bool{}
	for id := input.NodeID; ; {
		parent, generated := parents[id]
		if !generated {
			if _, exists := declarations[id]; !exists {
				return nil, executorError(apierr.PlanDrift, "generated job has no accepted source")
			}
			break
		}
		if visiting[id] {
			return nil, executorError(apierr.PlanDrift, "generator ancestry contains a cycle")
		}
		visiting[id] = true
		row, ok := ownerNode(snapshot, string(parent.Source.NodeID))
		if !ok || row.Generation != runspec.ClaimGeneration(parent.SourceGeneration) || row.Status != store.StatusDone || row.Outcome != store.NodeSuccess && row.Outcome != store.NodeMemoized {
			return nil, executorError(apierr.PlanDrift, "generator ancestry lost its successful source generation")
		}
		declaration := declarations[string(row.Ref.NodeID)]
		hash, err := runspec.CanonicalHash(declaration)
		if err != nil {
			return nil, err
		}
		if len(declaration.Closures.Generate) == 0 || declaration.Closures.Generate[0] == "" || !json.Valid(row.Spec) {
			return nil, executorError(apierr.PlanDrift, "generator ancestry has no persisted source generator")
		}
		persistedHash, err := runspec.CanonicalHash(row.Spec)
		if err != nil {
			return nil, err
		}
		if string(hash) != row.SpecHash || persistedHash != hash {
			return nil, executorError(apierr.PlanDrift, "generator ancestry differs from its persisted source declaration")
		}
		replay = append(replay, GeneratorReplay{SourceJob: string(row.Ref.NodeID), SourceGeneration: int64(row.Generation), GeneratorID: declaration.Closures.Generate[0]})
		id = string(parent.Source.NodeID)
	}
	slices.Reverse(replay)
	for i := range replay {
		if cause := context.Cause(ctx); cause != nil {
			return nil, cause
		}
		row, _ := ownerNode(snapshot, replay[i].SourceJob)
		replay[i].Output, err = h.readResult(ctx, row, input.Storage)
		if err != nil {
			return nil, err
		}
		if !json.Valid(replay[i].Output) {
			return nil, executorError(apierr.PlanDrift, "generator ancestry output is missing or invalid JSON")
		}
	}
	return replay, nil
}

func (h *sdkCode) replay(ctx context.Context, session *executorSession, input NodeInput, initial []nodeproto.JobSpec) ([]nodeproto.JobSpec, error) {
	jobs := append([]nodeproto.JobSpec(nil), initial...)
	if len(input.Replay) == 0 {
		return jobs, nil
	}
	snapshot, err := h.Store.ReadSnapshot(ctx, input.Accepted.RunID)
	if err != nil {
		return nil, err
	}
	if snapshot.Run.ID != input.Accepted.RunID {
		return nil, executorError(apierr.PlanDrift, "generator snapshot belongs to another run")
	}
	sources := map[string]GeneratorReplay{}
	for _, replay := range input.Replay {
		if replay.SourceGeneration < 0 || replay.SourceJob == "" || replay.GeneratorID == "" || !json.Valid(replay.Output) {
			return nil, executorError(apierr.PlanDrift, "generator replay lacks its persisted source identity or output")
		}
		if prior, ok := sources[replay.SourceJob]; ok && (prior.SourceGeneration != replay.SourceGeneration || !bytes.Equal(prior.Output, replay.Output)) {
			return nil, executorError(apierr.PlanDrift, "generator replay changed its recorded source")
		}
		sources[replay.SourceJob] = replay
	}
	completed := map[string]bool{}
	for _, replay := range input.Replay {
		var source *nodeproto.JobSpec
		for i := range jobs {
			if jobs[i].ID == replay.SourceJob {
				source = &jobs[i]
				break
			}
		}
		if source == nil || !executorContains(source.Closures.Generate, replay.GeneratorID) {
			return nil, executorError(apierr.PlanDrift, "generator replay names an undeclared source")
		}
		if completed[replay.SourceJob] {
			continue
		}
		generated, err := h.replaySource(ctx, session, input, *source, replay, snapshot, jobs)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, generated...)
		completed[replay.SourceJob] = true
	}
	return jobs, nil
}

func (h *sdkCode) replaySource(ctx context.Context, session *executorSession, input NodeInput, source nodeproto.JobSpec, replay GeneratorReplay, snapshot store.RunSnapshot, existing []nodeproto.JobSpec) ([]nodeproto.JobSpec, error) {
	expected, err := h.replayMembers(snapshot, replay)
	if err != nil {
		return nil, err
	}
	input.NodeID = replay.SourceJob
	body, err := h.generate(ctx, session, input, source, replay.Output)
	if err != nil {
		var invalid *apierr.Error
		if errors.As(err, &invalid) && invalid.Code == apierr.PlanInvalid {
			return nil, errors.Join(executorError(apierr.PlanDrift, "generator replay changed its accepted declarations"), err)
		}
		return nil, err
	}
	validation := h.Validation
	validation.Expansion, validation.SourceJob = true, source.ID
	validation.ExistingConcurrency = input.Accepted.Plan.Concurrency
	validation.ExistingJobs, err = replayTopology(existing, snapshot, expected)
	if err != nil {
		return nil, err
	}
	expansion, err := nodeproto.ValidatePlan(body, validation)
	if err != nil {
		return nil, errors.Join(executorError(apierr.PlanDrift, "generator replay no longer matches its accepted topology"), err)
	}
	if len(expansion.Jobs) != len(expected) {
		return nil, executorError(apierr.PlanDrift, "generator replay changed the accepted member set")
	}
	for _, generated := range expansion.Jobs {
		for _, job := range existing {
			if job.ID == generated.ID {
				return nil, executorError(apierr.PlanDrift, "generator replay overwrote an existing job")
			}
		}
		if !expected[generated.ID] {
			return nil, executorError(apierr.PlanDrift, "generator replay changed the accepted member set")
		}
		row, err := h.Store.ReadNode(ctx, controllerwire.NodeRef{RunID: runspec.RunID(input.Accepted.RunID), NodeID: runspec.NodeID(generated.ID)})
		if err != nil {
			return nil, executorError(apierr.PlanDrift, "generator replay added a job absent from the accepted expansion")
		}
		hash, err := runspec.CanonicalHash(generated)
		if err != nil {
			return nil, err
		}
		if string(hash) != row.SpecHash || row.Ref.RunID != runspec.RunID(input.Accepted.RunID) || row.Ref.NodeID != runspec.NodeID(generated.ID) {
			return nil, executorError(apierr.PlanDrift, "generator replay changed a persisted job")
		}
		delete(expected, generated.ID)
	}
	return expansion.Jobs, nil
}

func replayTopology(reconstructed []nodeproto.JobSpec, snapshot store.RunSnapshot, replacing map[string]bool) ([]nodeproto.JobSpec, error) {
	jobs := append([]nodeproto.JobSpec(nil), reconstructed...)
	seen := map[string]bool{}
	for _, job := range jobs {
		seen[job.ID] = true
	}
	for _, row := range snapshot.Nodes {
		if row.Ref.RunID != runspec.RunID(snapshot.Run.ID) || row.Ref.NodeID == "" {
			return nil, executorError(apierr.PlanDrift, "replay snapshot has a foreign job identity")
		}
		if replacing[string(row.Ref.NodeID)] || seen[string(row.Ref.NodeID)] {
			continue
		}
		var job nodeproto.JobSpec
		if err := json.Unmarshal(row.Spec, &job); err != nil || runspec.NodeID(job.ID) != row.Ref.NodeID {
			return nil, executorError(apierr.PlanDrift, "replay snapshot has an invalid job declaration")
		}
		jobs = append(jobs, job)
		seen[job.ID] = true
	}
	return jobs, nil
}

func (h *sdkCode) replayMembers(snapshot store.RunSnapshot, replay GeneratorReplay) (map[string]bool, error) {
	for _, accepted := range snapshot.Expansions {
		if accepted.Source.RunID != runspec.RunID(snapshot.Run.ID) || accepted.Source.NodeID != runspec.NodeID(replay.SourceJob) || accepted.SourceGeneration != replay.SourceGeneration {
			continue
		}
		var plan nodeproto.PlanDoc
		if err := json.Unmarshal(accepted.Body, &plan); err != nil {
			return nil, executorError(apierr.PlanDrift, "accepted expansion is malformed")
		}
		hash, err := runspec.CanonicalHash(plan)
		if err != nil {
			return nil, err
		}
		if string(hash) != accepted.BodyHash {
			return nil, executorError(apierr.PlanDrift, "accepted expansion hash differs from its body")
		}
		members := map[string]bool{}
		for _, job := range plan.Jobs {
			if job.ID == "" || members[job.ID] {
				return nil, executorError(apierr.PlanDrift, "accepted expansion has invalid member identities")
			}
			members[job.ID] = true
		}
		return members, nil
	}
	return nil, executorError(apierr.PlanDrift, "generator replay has no accepted expansion for its source generation")
}
