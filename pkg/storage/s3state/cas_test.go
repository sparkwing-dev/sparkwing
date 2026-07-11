package s3state_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// memCondArt is an in-memory ArtifactStore that also implements
// storage.ConditionalWriter with content-hash ETags, so the s3state CAS
// records run without a live object store. unsupported flips the live
// probe to false to exercise the last-write-wins fallback.
type memCondArt struct {
	mu          sync.Mutex
	data        map[string][]byte
	unsupported bool
}

func newMemCondArt() *memCondArt { return &memCondArt{data: map[string][]byte{}} }

func condETag(body []byte) storage.ETag {
	sum := sha256.Sum256(body)
	return storage.ETag(hex.EncodeToString(sum[:]))
}

func (m *memCondArt) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memCondArt) Put(_ context.Context, key string, r io.Reader) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = body
	return nil
}

func (m *memCondArt) Has(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.data[key]
	return ok, nil
}

func (m *memCondArt) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memCondArt) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.data))
	for k := range m.data {
		if prefix == "" || (len(k) >= len(prefix) && k[:len(prefix)] == prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

func (m *memCondArt) GetWithETag(_ context.Context, key string) (io.ReadCloser, storage.ETag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	if !ok {
		return nil, "", storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), condETag(b), nil
}

func (m *memCondArt) PutIfAbsent(_ context.Context, key string, r io.Reader) (storage.ETag, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[key]; ok {
		return "", storage.ErrPreconditionFailed
	}
	m.data[key] = body
	return condETag(body), nil
}

func (m *memCondArt) PutIfMatch(_ context.Context, key string, r io.Reader, expect storage.ETag) (storage.ETag, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.data[key]
	if !ok || condETag(cur) != expect {
		return "", storage.ErrPreconditionFailed
	}
	m.data[key] = body
	return condETag(body), nil
}

func (m *memCondArt) ConditionalWritesSupported(context.Context) (bool, error) {
	return !m.unsupported, nil
}

func newCASBackend(t *testing.T) *s3state.Backend {
	t.Helper()
	b, _ := newCASBackendWithArt(t)
	return b
}

// newCASBackendWithArt returns the backend plus the underlying CAS store
// so a test can count the discrete records the backend wrote.
func newCASBackendWithArt(t *testing.T) (*s3state.Backend, *memCondArt) {
	t.Helper()
	art := newMemCondArt()
	b := s3state.New(art)
	t.Cleanup(func() { _ = b.Close() })
	return b, art
}

// countKeys returns how many objects exist under prefix.
func (m *memCondArt) countKeys(prefix string) int {
	keys, _ := m.List(context.Background(), prefix)
	return len(keys)
}

func TestS3CAS_NodeDispatch_AutoSeqAndRead(t *testing.T) {
	b := newCASBackend(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := b.WriteNodeDispatch(ctx, store.NodeDispatch{RunID: "r", NodeID: "n", Seq: -1}); err != nil {
			t.Fatalf("WriteNodeDispatch[%d]: %v", i, err)
		}
	}
	list, err := b.ListNodeDispatches(ctx, "r", "n")
	if err != nil {
		t.Fatalf("ListNodeDispatches: %v", err)
	}
	if len(list) != 3 || list[0].Seq != 0 || list[1].Seq != 1 || list[2].Seq != 2 {
		t.Fatalf("seqs = %v, want 0,1,2 in order", seqsOf(list))
	}
	latest, err := b.GetNodeDispatch(ctx, "r", "n", -1)
	if err != nil || latest.Seq != 2 {
		t.Fatalf("latest = %+v err %v, want seq 2", latest, err)
	}
	one, err := b.GetNodeDispatch(ctx, "r", "n", 1)
	if err != nil || one.Seq != 1 {
		t.Fatalf("seq 1 = %+v err %v", one, err)
	}
	if _, err := b.GetNodeDispatch(ctx, "r", "n", 9); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing seq err = %v, want ErrNotFound", err)
	}
}

func TestS3CAS_NodeDispatch_ExplicitSeqRejectsDuplicate(t *testing.T) {
	b := newCASBackend(t)
	ctx := context.Background()
	if err := b.WriteNodeDispatch(ctx, store.NodeDispatch{RunID: "r", NodeID: "n", Seq: 5}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	err := b.WriteNodeDispatch(ctx, store.NodeDispatch{RunID: "r", NodeID: "n", Seq: 5})
	if err == nil {
		t.Fatal("duplicate seq write succeeded, want error")
	}
}

func TestS3CAS_NodeDispatch_TruncatesOversizeEnvelope(t *testing.T) {
	b := newCASBackend(t)
	ctx := context.Background()
	big := bytes.Repeat([]byte("x"), store.MaxNodeDispatchEnvelope+10)
	if err := b.WriteNodeDispatch(ctx, store.NodeDispatch{RunID: "r", NodeID: "n", Seq: 0, InputEnvelope: big}); err != nil {
		t.Fatalf("WriteNodeDispatch: %v", err)
	}
	got, err := b.GetNodeDispatch(ctx, "r", "n", 0)
	if err != nil {
		t.Fatalf("GetNodeDispatch: %v", err)
	}
	if got.InputSizeBytes != int64(len(big)) {
		t.Errorf("InputSizeBytes = %d, want %d (original preserved)", got.InputSizeBytes, len(big))
	}
	if len(got.InputEnvelope) >= len(big) || !bytes.Contains(got.InputEnvelope, []byte(`"truncated":true`)) {
		t.Errorf("envelope not truncated: %s", got.InputEnvelope)
	}
}

func TestS3CAS_NodeDispatch_ConcurrentAutoSeqNoCollision(t *testing.T) {
	b := newCASBackend(t)
	ctx := context.Background()
	const writers = 10
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := b.WriteNodeDispatch(ctx, store.NodeDispatch{RunID: "r", NodeID: "n", Seq: -1}); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent WriteNodeDispatch: %v", err)
	}
	list, err := b.ListNodeDispatches(ctx, "r", "n")
	if err != nil {
		t.Fatalf("ListNodeDispatches: %v", err)
	}
	if len(list) != writers {
		t.Fatalf("got %d dispatches, want %d", len(list), writers)
	}
	for i, d := range list {
		if d.Seq != i {
			t.Fatalf("seq[%d] = %d, want contiguous 0..%d (no over-admission)", i, d.Seq, writers-1)
		}
	}
}

func TestS3CAS_DebugPause_Lifecycle(t *testing.T) {
	b := newCASBackend(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := store.DebugPause{RunID: "r", NodeID: "n", Reason: "breakpoint", PausedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := b.CreateDebugPause(ctx, p); err != nil {
		t.Fatalf("CreateDebugPause: %v", err)
	}
	active, err := b.GetActiveDebugPause(ctx, "r", "n")
	if err != nil || active.Reason != "breakpoint" {
		t.Fatalf("GetActiveDebugPause = %+v err %v", active, err)
	}
	if err := b.ReleaseDebugPause(ctx, "r", "n", "alice", "manual"); err != nil {
		t.Fatalf("ReleaseDebugPause: %v", err)
	}
	if _, err := b.GetActiveDebugPause(ctx, "r", "n"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("active after release err = %v, want ErrNotFound", err)
	}
	if err := b.ReleaseDebugPause(ctx, "r", "n", "alice", "manual"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("release with no open pause err = %v, want ErrNotFound", err)
	}
	pauses, err := b.ListDebugPauses(ctx, "r")
	if err != nil || len(pauses) != 1 {
		t.Fatalf("ListDebugPauses = %d err %v, want 1 (released)", len(pauses), err)
	}
	if pauses[0].ReleasedAt == nil || pauses[0].ReleasedBy != "alice" {
		t.Errorf("released pause = %+v, want released_at set + released_by alice", pauses[0])
	}
}

func TestS3CAS_DebugPause_RecreateReopensReleased(t *testing.T) {
	b := newCASBackend(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := store.DebugPause{RunID: "r", NodeID: "n", Reason: "bp", PausedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := b.CreateDebugPause(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := b.ReleaseDebugPause(ctx, "r", "n", "bob", "manual"); err != nil {
		t.Fatal(err)
	}
	p.PausedAt = now.Add(time.Minute)
	if err := b.CreateDebugPause(ctx, p); err != nil {
		t.Fatalf("re-create: %v", err)
	}
	active, err := b.GetActiveDebugPause(ctx, "r", "n")
	if err != nil {
		t.Fatalf("active after recreate: %v", err)
	}
	if active.ReleasedAt != nil {
		t.Errorf("recreated pause still released: %+v", active)
	}
}

func TestS3CAS_Approval_ResolveAndDoubleResolve(t *testing.T) {
	b := newCASBackend(t)
	ctx := context.Background()
	a := store.Approval{RunID: "r", NodeID: "n", RequestedAt: time.Now().UTC(), Message: "ship?"}
	if err := b.CreateApproval(ctx, a); err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}
	got, err := b.GetApproval(ctx, "r", "n")
	if err != nil || got.OnTimeout != store.ApprovalOnTimeoutFail {
		t.Fatalf("GetApproval = %+v err %v, want OnTimeout default fail", got, err)
	}
	node, err := b.GetNode(ctx, "r", "n")
	if err != nil || node.Status != store.NodeStatusApprovalPending {
		t.Fatalf("node status = %v err %v, want approval_pending", nodeStatus(node), err)
	}
	resolved, err := b.ResolveApproval(ctx, "r", "n", "approved", "carol", "lgtm")
	if err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}
	if resolved.Resolution != "approved" || resolved.Approver != "carol" || resolved.ResolvedAt == nil {
		t.Errorf("resolved = %+v", resolved)
	}
	if _, err := b.ResolveApproval(ctx, "r", "n", "denied", "dave", ""); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("double resolve err = %v, want ErrLockHeld", err)
	}
}

func TestS3CAS_Approval_ResolveMissing(t *testing.T) {
	b := newCASBackend(t)
	if _, err := b.ResolveApproval(context.Background(), "r", "nope", "approved", "x", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("resolve missing err = %v, want ErrNotFound", err)
	}
}

func TestS3CAS_ListPendingApprovals_OrderedAndFiltered(t *testing.T) {
	b := newCASBackend(t)
	ctx := context.Background()
	base := time.Now().UTC()
	mk := func(run, node string, at time.Time) {
		if err := b.CreateApproval(ctx, store.Approval{RunID: run, NodeID: node, RequestedAt: at}); err != nil {
			t.Fatalf("CreateApproval %s/%s: %v", run, node, err)
		}
	}
	mk("r1", "a", base.Add(2*time.Second))
	mk("r2", "b", base.Add(1*time.Second))
	mk("r1", "c", base.Add(3*time.Second))
	if _, err := b.ResolveApproval(ctx, "r1", "c", "approved", "x", ""); err != nil {
		t.Fatal(err)
	}
	pend, err := b.ListPendingApprovals(ctx)
	if err != nil {
		t.Fatalf("ListPendingApprovals: %v", err)
	}
	if len(pend) != 2 {
		t.Fatalf("pending = %d, want 2 (resolved excluded)", len(pend))
	}
	if pend[0].NodeID != "b" || pend[1].NodeID != "a" {
		t.Errorf("order = %s,%s, want b,a (oldest requested first)", pend[0].NodeID, pend[1].NodeID)
	}
}

func TestS3CAS_EnqueueTrigger_IdempotentChild(t *testing.T) {
	b := newCASBackend(t)
	ctx := context.Background()
	first, err := b.EnqueueTrigger(ctx, "deploy", map[string]string{"k": "v"}, "parent-run", "node-1", "", "await-pipeline", "", "", "")
	if err != nil {
		t.Fatalf("EnqueueTrigger: %v", err)
	}
	if first == "" {
		t.Fatal("empty child run id")
	}
	again, err := b.EnqueueTrigger(ctx, "deploy", nil, "parent-run", "node-1", "", "await-pipeline", "", "", "")
	if err != nil {
		t.Fatalf("EnqueueTrigger repeat: %v", err)
	}
	if again != first {
		t.Errorf("repeat enqueue minted new id %s, want idempotent %s", again, first)
	}
	found, err := b.FindSpawnedChildTriggerID(ctx, "parent-run", "node-1", "deploy")
	if err != nil {
		t.Fatalf("FindSpawnedChildTriggerID: %v", err)
	}
	if found != first {
		t.Errorf("found = %s, want %s", found, first)
	}
}

func TestS3CAS_EnqueueTriggerWithEnvStoresTriggerEnv(t *testing.T) {
	b := newCASBackend(t)
	ctx := context.Background()
	id, err := b.EnqueueTriggerWithEnv(ctx,
		"deploy", nil, "parent-run", "node-1", "", "await-pipeline", "", "", "",
		map[string]string{
			"CHILD_CONTEXT": "from-parent",
			"CHILD_BRANCH":  "main",
		},
	)
	if err != nil {
		t.Fatalf("EnqueueTriggerWithEnv: %v", err)
	}
	trigger, err := b.GetTrigger(ctx, id)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if trigger.TriggerEnv["CHILD_CONTEXT"] != "from-parent" {
		t.Fatalf("trigger env CHILD_CONTEXT = %q, want from-parent", trigger.TriggerEnv["CHILD_CONTEXT"])
	}
	if trigger.TriggerEnv["CHILD_BRANCH"] != "main" {
		t.Fatalf("trigger env CHILD_BRANCH = %q, want main", trigger.TriggerEnv["CHILD_BRANCH"])
	}
}

func TestS3CAS_EnqueueTrigger_RequiresPipeline(t *testing.T) {
	b := newCASBackend(t)
	if _, err := b.EnqueueTrigger(context.Background(), "", nil, "", "", "", "", "", "", ""); err == nil {
		t.Fatal("empty pipeline enqueue succeeded, want error")
	}
}

func TestS3CAS_EnqueueTrigger_RejectsCycle(t *testing.T) {
	b := newCASBackend(t)
	ctx := context.Background()
	if err := b.CreateRun(ctx, store.Run{ID: "run-a", Pipeline: "loop", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	_, err := b.EnqueueTrigger(ctx, "loop", nil, "run-a", "n", "", "await-pipeline", "", "", "")
	if err == nil || !errContains(err, "cycle") {
		t.Fatalf("err = %v, want cycle rejection", err)
	}
}

func TestS3CAS_FindSpawnedChild_EmptyArgsReturnsEmpty(t *testing.T) {
	b := newCASBackend(t)
	id, err := b.FindSpawnedChildTriggerID(context.Background(), "", "n", "p")
	if err != nil || id != "" {
		t.Fatalf("id = %q err %v, want empty,nil for empty parent", id, err)
	}
}

func TestS3CAS_FallbackWhenEndpointIgnoresPreconditions(t *testing.T) {
	art := &memCondArt{data: map[string][]byte{}, unsupported: true}
	b := s3state.New(art)
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()

	cases := []struct {
		name string
		call func() error
	}{
		{"WriteNodeDispatch", func() error { return b.WriteNodeDispatch(ctx, store.NodeDispatch{RunID: "r", NodeID: "n"}) }},
		{"CreateDebugPause", func() error { return b.CreateDebugPause(ctx, store.DebugPause{RunID: "r", NodeID: "n"}) }},
		{"CreateApproval", func() error { return b.CreateApproval(ctx, store.Approval{RunID: "r", NodeID: "n"}) }},
		{"ResolveApproval", func() error { _, e := b.ResolveApproval(ctx, "r", "n", "approved", "x", ""); return e }},
		{"EnqueueTrigger", func() error { _, e := b.EnqueueTrigger(ctx, "p", nil, "", "", "", "", "", "", ""); return e }},
		{"FindSpawnedChildTriggerID", func() error { _, e := b.FindSpawnedChildTriggerID(ctx, "p", "n", "x"); return e }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, s3state.ErrNotSupported) {
				t.Errorf("err = %v, want ErrNotSupported (endpoint ignores preconditions)", err)
			}
		})
	}
}

// TestS3CAS_ConcurrentEnqueueTrigger_CoalescesToOneRecord exercises the
// PutIfAbsent child-trigger race: many goroutines spawn the same child
// from one (parentRun, node, pipeline). Exactly one trigger record must
// exist and every caller -- winner and losers alike -- must return that
// single run ID.
func TestS3CAS_ConcurrentEnqueueTrigger_CoalescesToOneRecord(t *testing.T) {
	b, art := newCASBackendWithArt(t)
	ctx := context.Background()
	const callers = 12

	var wg sync.WaitGroup
	ids := make([]string, callers)
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids[i], errs[i] = b.EnqueueTrigger(ctx, "deploy", nil, "parent-run", "node-1", "", "await-pipeline", "", "", "")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("EnqueueTrigger[%d]: %v", i, err)
		}
	}
	winner := ids[0]
	if winner == "" {
		t.Fatal("empty child run id")
	}
	for i, id := range ids {
		if id != winner {
			t.Fatalf("caller %d got id %q, want all callers coalesced to %q", i, id, winner)
		}
	}
	if n := art.countKeys("triggers/by-id/"); n != 1 {
		t.Fatalf("trigger records = %d, want exactly 1 (PutIfAbsent coalesce)", n)
	}
	if n := art.countKeys("triggers/child/"); n != 1 {
		t.Fatalf("child-trigger index records = %d, want exactly 1", n)
	}
	found, err := b.FindSpawnedChildTriggerID(ctx, "parent-run", "node-1", "deploy")
	if err != nil || found != winner {
		t.Fatalf("FindSpawnedChildTriggerID = %q err %v, want %q", found, err, winner)
	}
}

// TestS3CAS_ConcurrentResolveApproval_ExactlyOneWins exercises the
// PutIfMatch resolve race: many goroutines resolve one pending approval
// at once. Exactly one succeeds; the rest lose the compare-and-swap,
// re-read the now-resolved record, and report store.ErrLockHeld (the
// Mode 3 "already resolved" contract, not a raw ErrPreconditionFailed).
func TestS3CAS_ConcurrentResolveApproval_ExactlyOneWins(t *testing.T) {
	b := newCASBackend(t)
	ctx := context.Background()
	if err := b.CreateApproval(ctx, store.Approval{RunID: "r", NodeID: "n", RequestedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}

	const callers = 12
	var wg sync.WaitGroup
	results := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = b.ResolveApproval(ctx, "r", "n", "approved", "approver", "")
		}(i)
	}
	wg.Wait()

	wins, lockHeld := 0, 0
	for i, err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, store.ErrLockHeld):
			lockHeld++
		default:
			t.Fatalf("caller %d: unexpected err %v, want nil or ErrLockHeld", i, err)
		}
	}
	if wins != 1 {
		t.Fatalf("resolve winners = %d, want exactly 1", wins)
	}
	if lockHeld != callers-1 {
		t.Fatalf("ErrLockHeld losers = %d, want %d", lockHeld, callers-1)
	}
	got, err := b.GetApproval(ctx, "r", "n")
	if err != nil || got.Resolution != "approved" || got.ResolvedAt == nil {
		t.Fatalf("final approval = %+v err %v, want resolved approved", got, err)
	}
}

// TestS3CAS_ConcurrentDebugPauseCreateRelease_RaceSafe exercises the
// upsert-reset RMW and the release CAS under contention: concurrent
// creates of one pause settle to a single open record, then concurrent
// releases resolve it exactly once with no corruption.
func TestS3CAS_ConcurrentDebugPauseCreateRelease_RaceSafe(t *testing.T) {
	b := newCASBackend(t)
	ctx := context.Background()
	now := time.Now().UTC()
	pause := store.DebugPause{RunID: "r", NodeID: "n", Reason: "bp", PausedAt: now, ExpiresAt: now.Add(time.Hour)}

	const callers = 10
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := b.CreateDebugPause(ctx, pause); err != nil {
				t.Errorf("CreateDebugPause: %v", err)
			}
		}()
	}
	wg.Wait()

	if n := len(mustListPauses(t, b)); n != 1 {
		t.Fatalf("pause records after concurrent create = %d, want 1 (single key upsert)", n)
	}
	if _, err := b.GetActiveDebugPause(ctx, "r", "n"); err != nil {
		t.Fatalf("GetActiveDebugPause after concurrent create: %v", err)
	}

	releases := make([]error, callers)
	var wg2 sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg2.Add(1)
		go func(i int) {
			defer wg2.Done()
			releases[i] = b.ReleaseDebugPause(ctx, "r", "n", "releaser", "manual")
		}(i)
	}
	wg2.Wait()

	wins, notFound := 0, 0
	for i, err := range releases {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, store.ErrNotFound):
			notFound++
		default:
			t.Fatalf("release caller %d: unexpected err %v", i, err)
		}
	}
	if wins != 1 {
		t.Fatalf("release winners = %d, want exactly 1", wins)
	}
	if notFound != callers-1 {
		t.Fatalf("release ErrNotFound losers = %d, want %d", notFound, callers-1)
	}
	if _, err := b.GetActiveDebugPause(ctx, "r", "n"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("active pause after release err = %v, want ErrNotFound", err)
	}
	if n := len(mustListPauses(t, b)); n != 1 {
		t.Fatalf("pause records after release = %d, want 1 (released, not deleted)", n)
	}
}

func mustListPauses(t *testing.T, b *s3state.Backend) []*store.DebugPause {
	t.Helper()
	ps, err := b.ListDebugPauses(context.Background(), "r")
	if err != nil {
		t.Fatalf("ListDebugPauses: %v", err)
	}
	return ps
}

func seqsOf(ds []*store.NodeDispatch) []int {
	out := make([]int, len(ds))
	for i, d := range ds {
		out[i] = d.Seq
	}
	return out
}

func nodeStatus(n *store.Node) string {
	if n == nil {
		return "<nil>"
	}
	return n.Status
}

func errContains(err error, sub string) bool {
	return err != nil && bytes.Contains([]byte(err.Error()), []byte(sub))
}
