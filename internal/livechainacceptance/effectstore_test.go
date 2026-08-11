package livechainacceptance

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	storagefs "github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
)

type idempotentEffectAdapter struct {
	mu           sync.Mutex
	results      map[string]EffectResult
	creates      map[string]int
	loseResponse map[string]bool
}

func (adapter *idempotentEffectAdapter) Apply(_ context.Context, request EffectRequest) (EffectResult, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if existing, ok := adapter.results[request.ID]; ok {
		return existing, nil
	}
	result := EffectResult{Deployment: Deployment{EventID: request.Artifact.EventID, Commit: request.Artifact.Commit, Tree: request.Artifact.Tree, Digest: request.Artifact.Digest, UID: request.ID}}
	adapter.results[request.ID] = result
	adapter.creates[request.ID]++
	if adapter.loseResponse[request.ID] {
		delete(adapter.loseResponse, request.ID)
		return EffectResult{}, errors.New("response lost after durable external effect")
	}
	return result, nil
}

func (adapter *idempotentEffectAdapter) Reconcile(_ context.Context, request EffectRequest) (EffectResult, bool, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	result, ok := adapter.results[request.ID]
	return result, ok, nil
}

func TestDurableEffectExecutorRecoversResponseLossExactlyOnce(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	adapter := &idempotentEffectAdapter{
		results: make(map[string]EffectResult), creates: make(map[string]int),
		loseResponse: map[string]bool{"session/deploy_a": true},
	}
	executor, err := NewDurableEffectExecutor("acceptance", distributedTestStore{writer}, adapter)
	if err != nil {
		t.Fatal(err)
	}
	request := EffectRequest{ID: "session/deploy_a", Kind: EffectDeployA, Artifact: Artifact{EventID: "event", Commit: "0123456789abcdef0123456789abcdef01234567", Tree: "89abcdef0123456789abcdef0123456789abcdef", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	if _, err := executor.Apply(context.Background(), request); err == nil {
		t.Fatal("lost external response did not interrupt the first attempt")
	}
	result, err := executor.Apply(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deployment.UID != request.ID {
		t.Fatalf("recovered result = %+v", result)
	}
	adapter.mu.Lock()
	if adapter.creates[request.ID] != 1 {
		t.Fatalf("underlying effect creations = %d, want 1", adapter.creates[request.ID])
	}
	adapter.mu.Unlock()
	reconciled, found, err := executor.Reconcile(context.Background(), request)
	if err != nil || !found || !reflect.DeepEqual(reconciled, result) {
		t.Fatalf("reconciled result/found/error = %+v/%t/%v", reconciled, found, err)
	}
}

func TestDurableEffectExecutorRejectsConflictingStableRequest(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	adapter := &idempotentEffectAdapter{results: make(map[string]EffectResult), creates: make(map[string]int), loseResponse: make(map[string]bool)}
	executor, err := NewDurableEffectExecutor("acceptance", distributedTestStore{writer}, adapter)
	if err != nil {
		t.Fatal(err)
	}
	request := EffectRequest{ID: "session/deploy_a", Kind: EffectDeployA, Artifact: Artifact{EventID: "event", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	if _, err := executor.Apply(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	conflict := request
	conflict.Artifact.Digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := executor.Apply(context.Background(), conflict); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("conflicting request error = %v, want ErrSessionConflict", err)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.creates[request.ID] != 1 {
		t.Fatalf("conflicting request recreated effect %d times", adapter.creates[request.ID])
	}
}

func TestDurableEffectExecutorConcurrentApplyCreatesExternalEffectOnce(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	adapter := &idempotentEffectAdapter{results: make(map[string]EffectResult), creates: make(map[string]int), loseResponse: make(map[string]bool)}
	executor, err := NewDurableEffectExecutor("acceptance", distributedTestStore{writer}, adapter)
	if err != nil {
		t.Fatal(err)
	}
	request := EffectRequest{ID: "session/deploy_a", Kind: EffectDeployA, Artifact: Artifact{EventID: "event", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	const callers = 8
	var wait sync.WaitGroup
	errorsByCaller := make(chan error, callers)
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, applyErr := executor.Apply(context.Background(), request)
			errorsByCaller <- applyErr
		}()
	}
	wait.Wait()
	close(errorsByCaller)
	for applyErr := range errorsByCaller {
		if applyErr != nil {
			t.Fatal(applyErr)
		}
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.creates[request.ID] != 1 {
		t.Fatalf("underlying effect creations = %d, want 1", adapter.creates[request.ID])
	}
}

func TestDurableEffectExecutorFailsClosedOnStoredResultConflict(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := distributedTestStore{writer}
	adapter := &idempotentEffectAdapter{results: make(map[string]EffectResult), creates: make(map[string]int), loseResponse: make(map[string]bool)}
	executor, err := NewDurableEffectExecutor("acceptance", backend, adapter)
	if err != nil {
		t.Fatal(err)
	}
	request := EffectRequest{ID: "session/deploy_a", Kind: EffectDeployA, Artifact: Artifact{EventID: "event"}}
	if err := executor.persistIntent(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	original := EffectResult{Deployment: Deployment{UID: "original"}}
	encoded, err := encodeEffectRecord(original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.PutIfAbsent(context.Background(), executor.resultKey(request.ID), bytes.NewReader(encoded)); err != nil {
		t.Fatal(err)
	}
	adapter.results[request.ID] = EffectResult{Deployment: Deployment{UID: "different"}}
	result, err := executor.Apply(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, original) {
		t.Fatalf("result = %+v, want original %+v", result, original)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.creates[request.ID] != 0 {
		t.Fatalf("stored result invoked adapter %d times", adapter.creates[request.ID])
	}
}

func TestDurableEffectExecutorRejectsUnsupportedConditionalStore(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	adapter := &idempotentEffectAdapter{results: make(map[string]EffectResult), creates: make(map[string]int), loseResponse: make(map[string]bool)}
	executor, err := NewDurableEffectExecutor("acceptance", unsupportedDistributedStore{distributedTestStore{writer}}, adapter)
	if err != nil {
		t.Fatal(err)
	}
	request := EffectRequest{ID: "session/deploy_a", Kind: EffectDeployA}
	if _, err := executor.Apply(context.Background(), request); err == nil {
		t.Fatal("effect executor accepted a store without enforced conditional writes")
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.creates[request.ID] != 0 {
		t.Fatalf("unsupported store invoked adapter %d times", adapter.creates[request.ID])
	}
}
