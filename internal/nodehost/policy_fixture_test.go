package nodehost

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type testPolicyName struct{ Name string }

func setTestPolicies(h *OwnerHost, f func(context.Context, testPolicyName, EvaluationInput, store.NodeRow, StorageBinding) (ownerLayerResult, error)) {
	bind := func(name string) OwnerPolicy {
		return func(ctx context.Context, in EvaluationInput, row store.NodeRow, s StorageBinding) (ownerLayerResult, error) {
			return f(ctx, testPolicyName{name}, in, row, s)
		}
	}
	h.DebugPolicy, h.SkipPolicy = bind("debug"), bind("skip_if")
}

func setTestExecutor(d *ExecutorDependencies, f func(context.Context, testPolicyName, NodeInput, nodeproto.JobSpec) (ExecutorFinalizer, error)) {
	d.PrepareArtifacts = func(ctx context.Context, in NodeInput, j nodeproto.JobSpec) (ExecutorFinalizer, error) {
		return f(ctx, testPolicyName{"artifacts"}, in, j)
	}
	d.PrepareCaches = func(ctx context.Context, in NodeInput, j nodeproto.JobSpec) (ExecutorFinalizer, error) {
		return f(ctx, testPolicyName{"caches"}, in, j)
	}
}

func ownerOperationsFixture(h OwnerHost) OwnerOperations {
	return OwnerOperations{Renew: h.Renew, Acquire: h.Acquire, Prepare: h.Prepare, Project: h.Project, Decide: h.Decide, Wait: h.Wait, WaitNode: h.WaitNode, Debug: h.Debug, Local: h.Local, AdoptClaim: h.AdoptClaim, Delegator: h.Delegator, ExpansionActor: h.ExpansionActor, Expand: h.Expand}
}

func executionOperationsFixture(h ExecutorDependencies) ExecutionOperations {
	return ExecutionOperations{Log: h.Log, BindStorage: h.BindStorage, Bind: h.Bind, ChildContext: h.ChildContext, PrepareArtifacts: h.PrepareArtifacts, PrepareCaches: h.PrepareCaches, PublishOutput: h.PublishOutput, Annotate: h.Annotate, ReadFailure: h.ReadFailure}
}

func setTestNativePolicies(h *OwnerOperations, f func(context.Context, testPolicyName, EvaluationInput, store.NodeRow, StorageBinding) (ownerLayerResult, error)) {
	var host OwnerHost
	setTestPolicies(&host, f)
}
