package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// WithClient binds native services to the authenticated controller's common transports.
func WithClient(cfg NativeConfig, c *client.LocalHostClient) NativeConfig {
	cfg.ReadFailure = c.ReadFailure
	if _, remote := cfg.Store.(*RemoteHostStore); remote {
		cfg.ExpansionActor = func(ctx context.Context, input RunInput, row runstate.NodeRow) (runstate.Actor, error) {
			principal, err := cfg.Refresh(ctx)
			if err != nil {
				return runstate.Actor{}, err
			}
			binding := principal.Capability.Binding
			if principal.Authority != cfg.Principal.Authority || input.Owner.Authority != principal.Authority || input.Owner.CapabilityPrefix != principal.Capability.Prefix || principal.Capability.Unit != capability.UnitTrigger || binding.Kind != capability.BindingTrigger || input.Owner.Owner.RunID != runspec.RunID(binding.RunID) || input.Owner.Owner.Holder != binding.Holder || input.Owner.Owner.Generation != runspec.ClaimGeneration(binding.Generation) || row.Ref.RunID != runspec.RunID(input.RunID) || row.Ref.NodeID == "" {
				return runstate.Actor{}, errors.New("expansion source is outside the owner award")
			}
			return input.Owner, nil
		}
	}
	cfg.Storage = func(ctx context.Context, p capability.Principal) (StorageBinding, error) {
		storage, err := c.BindStorage(ctx, p)
		return StorageBinding{Bytes: storage.Bytes, Records: storage.Records, Authority: storage.Authority}, err
	}
	cfg.Logs = c.LogUploader
	cfg.Services = func(ctx context.Context, p capability.Principal, job nodewire.JobSpec) (ProcessServices, error) {
		remote, err := c.ProcessClient(ctx, p, job.ID)
		if err != nil {
			return ProcessServices{}, err
		}
		// perf: the process client already read this run's snapshot; a second read cost a controller round trip per job.
		service := ProcessServices{RunID: p.Capability.Binding.RunID, Job: job, Secrets: remote.SecretNames()}
		service.Annotation = func(ctx context.Context, _ capability.Authorized, body nodewire.AnnotationRequest) error {
			return remote.Annotation(ctx, body)
		}
		service.Summary = func(ctx context.Context, _ capability.Authorized, body nodewire.SummaryRequest) error {
			return remote.Summary(ctx, body)
		}
		service.Secret = func(ctx context.Context, _ capability.Authorized, name string) (string, bool, error) {
			return remote.Secret(ctx, name)
		}
		service.Output = func(ctx context.Context, _ capability.Authorized, job string) (json.RawMessage, error) {
			return remote.Output(ctx, service.RunID, job)
		}
		service.Latest = func(ctx context.Context, auth capability.Authorized, pipeline, job string, age time.Duration, fromAnyRun bool) (ProcessOutput, error) {
			trusted := auth.Target.Reference != nil && auth.Target.Reference.FromProvenRun
			run, output, err := remote.Latest(ctx, pipeline, job, age, fromAnyRun, trusted)
			return ProcessOutput{RunID: run, Output: output}, err
		}
		service.OIDC = func(ctx context.Context, _ capability.Authorized, audience string) (ProcessOIDCToken, error) {
			token, expiry, err := remote.OIDC(ctx, audience)
			return ProcessOIDCToken{Token: token, ExpiresAt: expiry}, err
		}
		service.StartChild = func(ctx context.Context, _ capability.Authorized, request ChildRequest) (string, error) {
			return remote.StartChild(ctx, request.Pipeline, request.Args, request.Repo, request.Branch, request.Detached)
		}
		service.AwaitChild = func(ctx context.Context, _ capability.Authorized, run, job string, wait time.Duration) (ProcessChildResult, error) {
			child, err := remote.Child(ctx, run, wait)
			result := ProcessChildResult{RunID: child.ID, Status: child.Status}
			if err == nil && child.FinishedAt != nil && job != "" {
				if child.Status == "success" || child.Status == "memoized" {
					result.Output, err = remote.ChildOutput(ctx, run, job)
				} else {
					source := child.Inputs.Git.Repository.Display
					if source == "" {
						source = string(child.Inputs.Git.Repository.Key)
					}
					if child.Inputs.Git.SHA != "" {
						source += "@" + child.Inputs.Git.SHA
					}
					code := apierr.SDKError
					if child.Reason == string(apierr.PlanFailed) {
						code = apierr.PlanFailed
					}
					result.Error = &apierr.Error{
						Code:    code,
						Message: fmt.Sprintf("child pipeline %q from source %q ended %s: %s. Check the pipeline name and definition in the requested source, then run the parent again.", child.Inputs.Pipeline, source, child.Status, child.Failure),
					}
				}
			}
			return result, err
		}
		return service, nil
	}
	return cfg
}
