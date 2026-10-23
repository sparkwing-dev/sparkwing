package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// PublicationInput supplies completed local state, verified bytes and controller-proxied logs.
type PublicationInput struct {
	State    runstate.LocalPublication
	OpenBlob func(context.Context, blob.Digest) (io.ReadCloser, int64, error)
	Logs     controllerwire.LogReader
}

// PublishLocalRun imports local history without admitting executable work.
func PublishLocalRun(ctx context.Context, options EngineOptions, input PublicationInput) (_ string, resultErr error) {
	local := input.State.Run
	if local.Spec == nil || local.FinishedAt == nil || local.Request.Request.Origin != runspec.OriginLocal || local.Request.Request.Mode != runspec.IntakeExecute {
		return "", dataProtocolError("publication requires completed local execution")
	}
	controller, err := NewEngineClient(options)
	if err != nil {
		return "", err
	}
	request := local.Request.Request
	request.Team, request.Actor, request.Mode = "", runspec.Actor{}, runspec.IntakePublish
	request.Source.Location = runspec.SourceLocation{}
	request.LocalSecretNames = nil
	request.LocalForeground = false
	request.Env = runspec.EnvSnapshot{}
	request.Lineage, request.Burst = runspec.Lineage{}, nil
	request.Options.Publish.Value, request.Options.CacheFrom.Value = "", ""
	response, err := controller.CreateRun(ctx, controllerwire.CreateRunRequest{Request: request, IdempotencyKey: "publication/" + local.ID})
	if err != nil {
		return "", err
	}
	write := response.WriteScope
	scope := capability.DataScope{Use: capability.DataPublish, RepositoryKey: string(response.Inputs.Git.Repository.Key), RunID: response.RunID, WriteRef: write, ReadRefs: []string{write}}
	publisher, err := OpenProfileData(ctx, options, scope)
	if err != nil {
		return response.RunID, err
	}
	defer func() { resultErr = errors.Join(resultErr, publisher.Close(context.WithoutCancel(ctx))) }()
	controller, err = NewEngineClient(publisher.Options)
	if err != nil {
		return response.RunID, err
	}
	plan, err := publicationPlan(input.State)
	if err != nil {
		return response.RunID, err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return response.RunID, err
	}
	resolved := local.Spec.Resolved
	if _, err := controller.AcceptPlan(ctx, response.RunID, controllerwire.PlanSubmission{Declarations: local.Spec.Declarations, Resolved: resolved, Plan: raw}); err != nil {
		return response.RunID, err
	}
	for _, node := range input.State.Nodes {
		if err := publishLocalJob(ctx, controller, publisher, input, node); err != nil {
			return response.RunID, err
		}
	}
	_, err = controller.Finish(ctx, response.RunID, controllerwire.FinishRequest{})
	return response.RunID, err
}

func publicationPlan(state runstate.LocalPublication) (nodeproto.PlanDoc, error) {
	var plan nodeproto.PlanDoc
	if err := json.Unmarshal(state.Run.Plan, &plan); err != nil {
		return plan, err
	}
	plan.Jobs = []nodeproto.JobSpec{}
	generated := map[string][]string{}
	for _, expansion := range state.Expansions {
		var body nodeproto.PlanDoc
		if err := json.Unmarshal(expansion.Body, &body); err != nil {
			return plan, err
		}
		for _, job := range body.Jobs {
			generated[string(expansion.Source.NodeID)] = append(generated[string(expansion.Source.NodeID)], job.ID)
		}
	}
	for _, node := range state.Nodes {
		var job nodeproto.JobSpec
		if err := json.Unmarshal(node.Spec, &job); err != nil {
			return plan, err
		}
		for _, source := range job.NeedsGenerated {
			job.Needs = append(job.Needs, generated[source]...)
		}
		slices.Sort(job.Needs)
		job.Needs = slices.Compact(job.Needs)
		job.NeedsGenerated, job.Closures.Generate = nil, nil
		plan.Jobs = append(plan.Jobs, job)
	}
	return plan, nil
}

func publishLocalJob(ctx context.Context, controller controllerwire.Controller, publisher ProfileData, input PublicationInput, node runstate.NodeRow) error {
	id := node.Ref.NodeID
	result := controllerwire.JobResult{Outcome: string(node.Outcome), Reason: node.StatusDetail}
	if stored, ok := input.State.Results[string(id)]; ok {
		result.Reason, result.MaskedError, result.Failure, result.ExitCode = stored.Reason, stored.MaskedError, stored.Failure, stored.ExitCode
	}
	if node.Attempt > 0 || node.OutputRecord != nil || node.ManifestRecord != nil {
		if node.FinishedAt == nil {
			return dataProtocolError("local job has no terminal timestamp")
		}
		started := input.State.Run.CreatedAt
		if node.StartedAt != nil {
			started = *node.StartedAt
		}
		attempt := runspec.AttemptIdentity{Team: publisher.Team, RunID: publisher.Authority.Upload.Value.RunID, NodeID: string(id), Attempt: int64(max(1, node.Attempt)), Generation: int64(max(1, node.Generation))}
		imported := runspec.PublishedAttempt{Attempt: attempt.Attempt, Generation: attempt.Generation, StartedAt: started, FinishedAt: *node.FinishedAt, Outcome: result.Outcome, Executor: "local"}
		if _, err := controller.TransitionJob(ctx, attempt.RunID, string(id), controllerwire.JobTransitionRequest{To: "pending", Cause: "publish_attempt", Publication: &imported}); err != nil {
			return err
		}
		bytes, records, authority, err := publisher.PublicationStorage(attempt)
		if err != nil {
			return err
		}
		for _, selected := range input.State.Records {
			if runspec.NodeID(selected.Record.Key.Scope.Job) != id {
				continue
			}
			key, err := publishLocalRecord(ctx, bytes, records, authority, attempt, input.OpenBlob, selected)
			if err != nil {
				return err
			}
			if key.Kind == blob.KindOutput {
				result.Output = &key
			} else if key.Kind == blob.KindManifest {
				result.Manifest = &key
			}
		}
		if input.Logs != nil {
			if err := publishLocalLogs(ctx, publisher, input.Logs, node.Ref, attempt); err != nil {
				return err
			}
		}
	}
	_, err := controller.TransitionJob(ctx, publisher.Authority.Upload.Value.RunID, string(id), controllerwire.JobTransitionRequest{To: result.Outcome, Cause: "publish", Result: &result})
	return err
}

func publishLocalRecord(ctx context.Context, bytes blob.Store, records blob.RecordStore, authority blob.Authority, attempt runspec.AttemptIdentity, open func(context.Context, blob.Digest) (io.ReadCloser, int64, error), selected blob.RecordLookupResult) (blob.RecordKey, error) {
	key := blob.ResultRecordKey(attempt, authority.Repo, authority.Ref.Scope, selected.Record.Key.Scope.Pipeline, selected.Record.Key.Kind)
	if open == nil {
		return key, dataProtocolError("publication has no local byte reader")
	}
	digests := []blob.Digest{selected.Record.Digest}
	if selected.Record.ManifestDigest.Set {
		digests = append(digests, selected.Record.ManifestDigest.Value)
	}
	for _, member := range selected.Members {
		digests = append(digests, member.Digest)
	}
	for _, digest := range digests {
		body, size, err := open(ctx, digest)
		if err != nil {
			return key, err
		}
		_, putErr := bytes.Put(ctx, blob.PutRequest{Identity: authority.Upload.Value, Authority: authority, Digest: digest, Size: size, Body: body})
		if err := errors.Join(putErr, body.Close()); err != nil {
			return key, err
		}
	}
	members := slices.Clone(selected.Members)
	for i := range members {
		members[i].Team = attempt.Team
	}
	_, err := records.CommitRecord(ctx, blob.CommitRecordRequest{Authority: authority, Source: authority.Upload.Value, Key: key, Digest: selected.Record.Digest, ManifestDigest: selected.Record.ManifestDigest, Members: members})
	return key, err
}

func publishLocalLogs(ctx context.Context, publisher ProfileData, reader controllerwire.LogReader, ref runspec.NodeRef, attempt runspec.AttemptIdentity) (resultErr error) {
	uploader, err := publisher.PublicationLogs(attempt)
	if err != nil {
		return err
	}
	subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: attempt.Team, RunID: attempt.RunID, Attempt: &attempt}
	sink, err := logseg.NewSink(ctx, uploader, logseg.Options{Subject: subject})
	if err != nil {
		return err
	}
	defer func() {
		_, err := sink.Close(ctx, logseg.End{Stopped: resultErr != nil})
		resultErr = errors.Join(resultErr, err)
	}()
	cursor := ""
	for {
		page, err := reader.ReadLogs(ctx, string(ref.RunID), controllerwire.LogQuery{Job: string(ref.NodeID), Cursor: cursor, NoEvents: true})
		if err != nil {
			return err
		}
		for _, record := range page.Records {
			if record.Subject == nil || record.Subject.Attempt == nil {
				continue
			}
			if err := sink.Append([]byte(record.Line+"\n"), "", 0, 0); err != nil {
				return err
			}
		}
		if page.Complete && !page.Truncated {
			return nil
		}
		if !page.Truncated {
			return dataProtocolError("completed local run has incomplete retained logs")
		}
		cursor = page.NextCursor
	}
}
