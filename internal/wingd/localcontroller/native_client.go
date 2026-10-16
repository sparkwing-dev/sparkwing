package localcontroller

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// WithClient binds native services to the authenticated controller's common transports.
func WithClient(cfg NativeConfig, c *client.LocalHostClient) NativeConfig {
	cfg.Storage = func(ctx context.Context, p capability.Principal) (nodehost.StorageBinding, error) {
		storage, err := c.BindStorage(ctx, p)
		return nodehost.StorageBinding{Bytes: storage.Bytes, Records: storage.Records, Authority: storage.Authority}, err
	}
	cfg.Logs = c.LogUploader
	cfg.Services = func(ctx context.Context, p capability.Principal, job nodeproto.JobSpec) (nodehost.ProcessServices, error) {
		remote, err := c.ProcessClient(ctx, p, job.ID)
		if err != nil {
			return nodehost.ProcessServices{}, err
		}
		snapshot, err := cfg.Store.ReadSnapshot(ctx, p.Capability.Binding.RunID)
		if err != nil {
			return nodehost.ProcessServices{}, err
		}
		service := nodehost.ProcessServices{RunID: p.Capability.Binding.RunID, Job: job}
		if snapshot.Run.Spec != nil {
			service.Secrets = append([]string{}, snapshot.Run.Spec.Resolved.SecretNames...)
		}
		service.Annotation = func(ctx context.Context, _ capability.Authorized, body nodeproto.AnnotationRequest) error {
			return remote.Annotation(ctx, body)
		}
		service.Summary = func(ctx context.Context, _ capability.Authorized, body nodeproto.SummaryRequest) error {
			return remote.Summary(ctx, body)
		}
		service.Secret = func(ctx context.Context, _ capability.Authorized, name string) (string, bool, error) {
			return remote.Secret(ctx, name)
		}
		service.Output = func(ctx context.Context, _ capability.Authorized, job string) (json.RawMessage, error) {
			row, err := cfg.Store.ReadNode(ctx, store.NodeRef{RunID: service.RunID, NodeID: job})
			if err != nil {
				return nil, err
			}
			if row.OutputRecord == nil {
				return nil, blob.ErrNotFound
			}
			storage, err := cfg.Storage(ctx, p)
			if err != nil {
				return nil, err
			}
			return (nodehost.OutputRecords{Blobs: storage.Bytes, Records: storage.Records}).ReadOutput(ctx, blob.RecordLookup{Authority: storage.Authority, Key: *row.OutputRecord})
		}
		service.Latest = func(ctx context.Context, _ capability.Authorized, pipeline, job string, age time.Duration) (nodehost.ProcessOutput, error) {
			run, output, err := remote.Latest(ctx, pipeline, job, age)
			return nodehost.ProcessOutput{RunID: run, Output: output}, err
		}
		service.OIDC = func(ctx context.Context, _ capability.Authorized, audience string) (nodehost.ProcessOIDCToken, error) {
			token, expiry, err := remote.OIDC(ctx, audience)
			return nodehost.ProcessOIDCToken{Token: token, ExpiresAt: expiry}, err
		}
		service.StartChild = func(ctx context.Context, _ capability.Authorized, request nodehost.ChildRequest) (string, error) {
			return remote.StartChild(ctx, request.Pipeline, request.Args, request.Repo, request.Branch)
		}
		service.AwaitChild = func(ctx context.Context, _ capability.Authorized, run, job string, wait time.Duration) (nodehost.ProcessChildResult, error) {
			child, err := remote.Child(ctx, run, wait)
			result := nodehost.ProcessChildResult{RunID: child.ID, Status: child.Status}
			if err == nil && child.FinishedAt != nil && job != "" {
				result.Output, err = remote.Output(ctx, run, job)
			}
			return result, err
		}
		return service, nil
	}
	return cfg
}
