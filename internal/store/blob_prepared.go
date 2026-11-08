package store

import (
	"context"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func preparedParentTx(ctx context.Context, tx *storeTx, a blobActor) (runspec.Request, bool, error) {
	child := a.request
	if child.Lineage.ParentRunID == "" || !runspec.IsCommitID(child.Git.SHA) || !runspec.IsCommitID(child.Source.Ref) {
		return runspec.Request{}, false, nil
	}
	parent, err := readRunTx(ctx, tx, Team(a.capability.Team), child.Lineage.ParentRunID)
	if err != nil {
		return runspec.Request{}, false, err
	}
	from := parent.Request.Request
	childSource, parentSource := child.Source, from.Source
	childSource.Entrypoint, parentSource.Entrypoint = "", ""
	childSource.RequestedRef, parentSource.RequestedRef = "", ""
	// safety: lineage is sealed by authenticated child intake. Arguments and pipeline names
	// select execution from the same entry, while all acquisition inputs must stay identical.
	eligible := child.Git.Repository.Key == from.Git.Repository.Key && child.Git.SHA == from.Git.SHA &&
		child.Git.Ref == from.Git.Ref && child.Git.DefaultBranch == from.Git.DefaultBranch &&
		childSource == parentSource && reflect.DeepEqual(child.Options.Checkout.Value.Policy, from.Options.Checkout.Value.Policy)
	return from, eligible, nil
}
