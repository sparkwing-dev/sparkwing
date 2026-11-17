package nodehost

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestRetriedCacheJobPersistsItsCacheAnnotation(t *testing.T) {
	cacheInput, adapter, backend, _ := depcacheFixture(t)
	workspace := cacheInput.Entry.Directory
	var fixture *storeHostFixture
	var trace []string
	var processes []*scriptedSDK
	refused := 0
	_, _, host := executionHostFixture(t, "cache_retry", &trace, &processes, func(deps *ExecutorDependencies) {
		fixture = executionStoreFixture(t, deps)
		start := deps.Start
		deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
			if in.Attempt == 1 {
				writeVendor(t, workspace)
			}
			return start(ctx, in, activity)
		}
		deps.BindStorage = func(_ context.Context, in NodeInput) (StorageBinding, error) {
			team, repo := string(store.DefaultTeam), runspec.RepoKey("gh:1")
			backend.upload.Identity.Team, backend.upload.Identity.RunID, backend.upload.Key.Team = team, in.Accepted.RunID, team
			backend.upload.Authority.Repo, backend.upload.Key.Scope.Repo = repo, repo
			backend.upload.Identity.Attempt.Value = runspec.AttemptIdentity{Team: team, RunID: in.Accepted.RunID, NodeID: in.NodeID, Generation: int64(in.Generation), Attempt: int64(in.Attempt)}
			backend.upload.Authority.Attempt = backend.upload.Identity.Attempt
			backend.upload.Authority.Upload.Value = backend.upload.Identity
			return StorageBinding{Bytes: backend, Records: backend, Authority: backend.upload.Authority}, nil
		}
		deps.PrepareCaches = adapter.Prepare
		deps.Annotate = func(ctx context.Context, in NodeInput, body nodewire.AnnotationRequest) error {
			ref := runspec.NodeRef{RunID: runspec.RunID(in.Accepted.RunID), NodeID: runspec.NodeID(in.NodeID)}
			row, err := fixture.Tenant.ReadNode(ctx, ref)
			if err != nil {
				return err
			}
			if row.Attempt != in.Attempt || row.Generation != in.Generation {
				refused++
				return nodeClaimMismatch()
			}
			return fixture.Tenant.WriteAnnotation(ctx, store.AnnotationWrite{Ref: ref, Actor: fixture.Actor, Annotation: body.Annotation})
		}
	})
	input := storedExecutionInput(t, fixture, host)
	host.Store, host.Masker = fixture.Tenant, testMasker("private")
	acquire := host.Acquire
	host.Acquire = func(ctx context.Context, spec source.Spec, options source.Options) (*source.Entry, error) {
		entry, err := acquire(ctx, spec, options)
		if entry != nil {
			entry.Directory = workspace
		}
		return entry, err
	}
	if err := host.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	ref := runspec.NodeRef{RunID: runspec.RunID(input.Accepted.RunID), NodeID: "job"}
	row, err := fixture.Tenant.ReadNode(t.Context(), ref)
	if err != nil || row.Attempt < 2 || row.Outcome != runstate.NodeSuccess {
		t.Fatalf("job did not succeed on a retry: attempt=%d outcome=%q err=%v", row.Attempt, row.Outcome, err)
	}
	nodes, err := fixture.Tenant.ListNodes(t.Context(), input.Accepted.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if node.NodeID != "job" {
			continue
		}
		if refused != 0 || len(node.Annotations) != 1 || !strings.HasPrefix(node.Annotations[0].Message, "cache dependencies (vendor): miss (key ") || !strings.Contains(node.Annotations[0].Message, "; saved ") {
			t.Fatalf("attempt %d stored annotations %+v after %d refused writes", row.Attempt, node.Annotations, refused)
		}
		return
	}
	t.Fatal("job node missing")
}
