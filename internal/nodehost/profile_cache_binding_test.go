package nodehost

import (
	"context"
	"encoding/json"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type profileCacheBinding struct{ blob.RemoteCache }

func TestFreshAttemptStoragePreservesOnlySealedProfileCache(t *testing.T) {
	cache := &profileCacheBinding{}
	property := func(local, opted, fresh bool) bool {
		spec := runspec.Spec{Request: runspec.SealedRequest{Request: runspec.Request{Team: "team", Git: runspec.Git{Repository: runspec.Repository{Key: "repo"}}}}}
		if local {
			spec.Request.Request.Origin = runspec.OriginLocal
		}
		if opted {
			spec.Request.Request.Options.CacheFrom.Value = "ci"
		}
		raw, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		input := NodeInput{Accepted: AcceptedRun{RunID: "run", Spec: raw}, NodeID: "job", Attempt: 2, Generation: 3, Storage: StorageBinding{RemoteCache: cache}}
		executor := nodeExecutor{deps: ExecutorDependencies{BindStorage: func(context.Context, NodeInput) (StorageBinding, error) {
			binding := StorageBinding{Bytes: executorStorageBytes{}, Records: executorStorageRecords{}, Authority: blob.Authority{CapabilityRef: "claim", Principal: "executor", Repo: "repo", Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: "team", RunID: "run", NodeID: "job", Attempt: 2, Generation: 3}}}}
			if fresh {
				binding.RemoteCache = cache
			}
			return binding, nil
		}}}
		bound, err := executor.bindStorage(t.Context(), input)
		return err == nil && (bound.Storage.RemoteCache == cache) == (local && opted)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}
