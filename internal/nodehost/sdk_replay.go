package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
	expansion, err := h.ValidatePlan(body, validation)
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
		row, err := h.Store.ReadNode(ctx, store.NodeRef{RunID: input.Accepted.RunID, NodeID: generated.ID})
		if err != nil {
			return nil, executorError(apierr.PlanDrift, "generator replay added a job absent from the accepted expansion")
		}
		hash, err := h.CanonicalHash(generated)
		if err != nil {
			return nil, err
		}
		if string(hash) != row.SpecHash || row.Ref.RunID != input.Accepted.RunID || row.Ref.NodeID != generated.ID {
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
		if row.Ref.RunID != snapshot.Run.ID || row.Ref.NodeID == "" {
			return nil, executorError(apierr.PlanDrift, "replay snapshot has a foreign job identity")
		}
		if replacing[row.Ref.NodeID] || seen[row.Ref.NodeID] {
			continue
		}
		var job nodeproto.JobSpec
		if err := json.Unmarshal(row.Spec, &job); err != nil || job.ID != row.Ref.NodeID {
			return nil, executorError(apierr.PlanDrift, "replay snapshot has an invalid job declaration")
		}
		jobs = append(jobs, job)
		seen[job.ID] = true
	}
	return jobs, nil
}

func (h *sdkCode) replayMembers(snapshot store.RunSnapshot, replay GeneratorReplay) (map[string]bool, error) {
	for _, accepted := range snapshot.Expansions {
		if accepted.Source.RunID != snapshot.Run.ID || accepted.Source.NodeID != replay.SourceJob || accepted.SourceGeneration != replay.SourceGeneration {
			continue
		}
		var plan nodeproto.PlanDoc
		if err := json.Unmarshal(accepted.Body, &plan); err != nil {
			return nil, executorError(apierr.PlanDrift, "accepted expansion is malformed")
		}
		hash, err := h.CanonicalHash(plan)
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
