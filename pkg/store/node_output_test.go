package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func commitOutput(ctx context.Context, t *testing.T, s *store.Store, team store.Team, runID, nodeID string, data []byte) store.OutputRef {
	t.Helper()
	key, err := s.NodeOutputKey(ctx, runID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	sha := digestOf(data)
	u, err := s.ReserveUpload(ctx, store.UploadRequest{
		Team: team, RunID: runID, Kind: store.StorageCache, Key: key, Size: int64(len(data)),
		SHA256: sha, Principal: runID + "/" + nodeID, Provenance: "cloud",
	})
	if err != nil {
		t.Fatalf("reserve output: %v", err)
	}
	if err := s.CommitUpload(ctx, team, u.ID, u.Principal, time.Now()); err != nil {
		t.Fatalf("commit output: %v", err)
	}
	return store.OutputRef{Key: key, Size: int64(len(data)), SHA256: sha}
}

func TestReportAttempt_RecordsOnlyItsOwnCommittedOutput(t *testing.T) {
	f := newDispatchRun(t, "run-output-ref")
	f.mustAccept(t, planOf("a", "b"))
	tok := f.claim(t, "a", store.ClaimTokenWork)
	own := commitOutput(t.Context(), t, f.s, store.DefaultTeam, f.run, "a", []byte(`{"v":1}`))
	other := commitOutput(t.Context(), t, f.s, store.DefaultTeam, f.run, "b", []byte(`{"v":2}`))
	uncommitted, _ := f.s.NodeOutputKey(t.Context(), f.run, "a")

	refused := map[string]store.OutputRef{
		"another node's object": other,
		"an uncommitted key":    {Key: uncommitted, Size: own.Size, SHA256: own.SHA256},
		"a different size":      {Key: own.Key, Size: own.Size + 1, SHA256: own.SHA256},
		"a different digest":    {Key: own.Key, Size: own.Size, SHA256: other.SHA256},
	}
	for name, ref := range refused {
		if _, err := f.report(tok, store.AttemptReport{Outcome: "success", Output: &ref}); !errors.Is(err, store.ErrAttemptInvalid) {
			t.Fatalf("report naming %s: err = %v, want refused", name, err)
		}
	}
	if _, err := f.report(tok, store.AttemptReport{Outcome: "success", Output: &own}); err != nil {
		t.Fatalf("report naming its own committed output: %v", err)
	}
	if a := f.node(t, "a"); a.OutputRef == nil || *a.OutputRef != own {
		t.Fatalf("node output ref = %+v, want %+v", a.OutputRef, own)
	}
	obj, err := f.s.NodeOutputObject(t.Context(), store.DefaultTeam, f.run, "a")
	if err != nil || obj.SHA256 != own.SHA256 || obj.Size != own.Size {
		t.Fatalf("output object = %+v, %v", obj, err)
	}
	if _, err := f.s.NodeOutputObject(t.Context(), store.DefaultTeam, f.run, "b"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a committed object no report named is readable as b's output: %v", err)
	}
}

func TestFinishNodeWithOutputRef_RefusesAnotherRunsObject(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	for _, run := range []string{"run-fin-a", "run-fin-b"} {
		if err := s.CreateRun(ctx, store.Run{ID: run, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateNode(ctx, store.Node{RunID: run, NodeID: "n", Status: "running"}); err != nil {
			t.Fatal(err)
		}
	}
	foreign := commitOutput(t.Context(), t, s, store.DefaultTeam, "run-fin-b", "n", []byte(`"b"`))
	if err := s.FinishNodeWithOutputRef(ctx, "run-fin-a", "n", "success", "", &foreign, "", nil); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("finish naming another run's output: err = %v, want refused", err)
	}
	own := commitOutput(t.Context(), t, s, store.DefaultTeam, "run-fin-a", "n", []byte(`"a"`))
	if err := s.FinishNodeWithOutputRef(ctx, "run-fin-a", "n", "success", "", &own, "", nil); err != nil {
		t.Fatalf("finish naming its own output: %v", err)
	}
}

func TestReportAttempt_ARetryDropsTheFailedAttemptsOutput(t *testing.T) {
	f := newDispatchRun(t, "run-output-retry")
	f.mustAccept(t, planOf(`a|"modifiers":{"retry":1,"retry_auto":true}`))
	tok := f.claim(t, "a", store.ClaimTokenWork)
	ref := commitOutput(t.Context(), t, f.s, store.DefaultTeam, f.run, "a", []byte(`{"try":1}`))
	if _, err := f.report(tok, store.AttemptReport{Outcome: "failed", Output: &ref}); err != nil {
		t.Fatal(err)
	}
	a := f.node(t, "a")
	if a.Status == "done" {
		t.Fatalf("the failed attempt was not retried: %+v", a)
	}
	if a.OutputRef != nil {
		t.Fatalf("a retried node kept its failed attempt's output: %+v", a.OutputRef)
	}
	retry := f.claim(t, "a", store.ClaimTokenWork)
	if _, err := f.report(retry, store.AttemptReport{Outcome: "success", Output: &ref}); !errors.Is(err, store.ErrAttemptInvalid) {
		t.Fatalf("a retry naming the failed attempt's object: err = %v, want refused", err)
	}
	fresh := commitOutput(t.Context(), t, f.s, store.DefaultTeam, f.run, "a", []byte(`{"try":2}`))
	if _, err := f.report(retry, store.AttemptReport{Outcome: "success", Output: &fresh}); err != nil {
		t.Fatalf("a retry naming its own object: %v", err)
	}
}

func TestReserveUpload_EnforcesOutputLimits(t *testing.T) {
	s := storetest.Open(t)
	req := func(run string, size int64) error {
		key, _ := store.NewOutputKey(run, "n", 1, 0)
		_, err := s.ReserveUpload(t.Context(), store.UploadRequest{
			Team: store.DefaultTeam, RunID: run, Kind: store.StorageCache, Key: key, Size: size,
			SHA256: strings.Repeat("a", 64), Principal: "p", Provenance: "cloud",
		})
		return err
	}
	if err := req("run-limit", store.MaxOutputBytes+1); !errors.Is(err, store.ErrOutputLimit) {
		t.Fatalf("an output over 64 MiB: err = %v, want refused", err)
	}
	for i := int64(0); i < store.MaxRunOutputBytes/store.MaxOutputBytes; i++ {
		if err := req("run-limit", store.MaxOutputBytes); err != nil {
			t.Fatalf("output %d within the run's 1 GiB: %v", i, err)
		}
	}
	if err := req("run-limit", 1); !errors.Is(err, store.ErrOutputLimit) {
		t.Fatalf("a byte past the run's 1 GiB: err = %v, want refused", err)
	}
	if err := req("run-other", store.MaxOutputBytes); err != nil {
		t.Fatalf("another run is charged for the first run's outputs: %v", err)
	}
	key, _ := store.NewOutputKey("run-other", "n", 1, 0)
	if _, err := s.ReserveUpload(t.Context(), store.UploadRequest{
		Team: store.DefaultTeam, RunID: "run-limit", Kind: store.StorageCache, Key: key, Size: 1,
		SHA256: strings.Repeat("a", 64), Principal: "p", Provenance: "cloud",
	}); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("a reservation for another run's key: err = %v, want refused", err)
	}
}

func TestReserveUpload_OutputsCountTowardTheTeamShare(t *testing.T) {
	s := storetest.Open(t)
	setFreeAllowance(t, s, 16<<10)
	freeTeam(t, s, "team-out")
	reserve := func(size int64) error {
		key, _ := store.NewOutputKey("run-share", "n", 1, 0)
		_, err := s.ReserveUpload(t.Context(), store.UploadRequest{
			Team: "team-out", RunID: "run-share", Kind: store.StorageCache, Key: key, Size: size,
			SHA256: strings.Repeat("a", 64), Principal: "p", Provenance: "cloud",
		})
		return err
	}
	if err := reserve(1 << 10); err != nil {
		t.Fatalf("an output within the share: %v", err)
	}
	if err := reserve(store.MaxUnpaidOutputBytes + 1); !errors.Is(err, store.ErrStorageQuota) {
		t.Fatalf("an output past the share: err = %v, want a storage refusal", err)
	}
}

func TestNodeIsAncestor_FollowsDepsTransitively(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	if err := s.CreateRun(ctx, store.Run{ID: "run-anc", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for id, deps := range map[string][]string{"a": nil, "b": {"a"}, "c": {"b"}, "d": nil} {
		if err := s.CreateNode(ctx, store.Node{RunID: "run-anc", NodeID: id, Status: "pending", Deps: deps}); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		ancestor, node string
		want           bool
	}{
		{"a", "c", true}, {"b", "c", true}, {"d", "c", false}, {"c", "a", false}, {"c", "c", false},
	}
	for _, tc := range cases {
		got, err := s.NodeIsAncestor(ctx, store.DefaultTeam, "run-anc", tc.ancestor, tc.node)
		if err != nil || got != tc.want {
			t.Errorf("NodeIsAncestor(%s, %s) = %v, %v; want %v", tc.ancestor, tc.node, got, err, tc.want)
		}
	}
}

func TestFinishNode_KeepsLocalOutputBytesBesideTheStore(t *testing.T) {
	s := storetest.Open(t)
	s.SetOutputDir(t.TempDir())
	ctx := context.Background()
	if err := s.CreateRun(ctx, store.Run{ID: "run-local", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-local", NodeID: "n", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishNode(ctx, "run-local", "n", "success", "", []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNodeOutput(ctx, "run-local", "n")
	if err != nil || string(got) != `{"ok":true}` {
		t.Fatalf("output = %s, %v", got, err)
	}
	n, _ := s.GetNode(ctx, "run-local", "n")
	if err := os.WriteFile(store.OutputPath(s.OutputDir(), n.OutputRef.Key), []byte(`{"ok":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetNodeOutput(ctx, "run-local", "n"); err == nil {
		t.Fatal("a tampered output file read back without error")
	}
	s.SetOutputDir("")
	if err := s.FinishNode(ctx, "run-local", "n", "success", "", []byte(`1`)); !errors.Is(err, store.ErrNoOutputDir) {
		t.Fatalf("output bytes on a store with no output dir: err = %v", err)
	}
}

func TestFinishNode_HoldsALocalRunToItsOutputLimit(t *testing.T) {
	s := storetest.Open(t)
	s.SetOutputDir(t.TempDir())
	ctx := t.Context()
	if err := s.CreateRun(ctx, store.Run{ID: "run-full", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"big", "last"} {
		if err := s.CreateNode(ctx, store.Node{RunID: "run-full", NodeID: n, Status: "running"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range store.MaxRunOutputBytes / store.MaxOutputBytes {
		key, _ := s.NodeOutputKey(ctx, "run-full", "big")
		ref := store.OutputRef{Key: key, Size: store.MaxOutputBytes, SHA256: fmt.Sprintf("%064x", i)}
		if err := s.RecordLocalOutput(ctx, "run-full", "big", ref, time.Now()); err != nil {
			t.Fatalf("output %d within the run's 1 GiB: %v", i, err)
		}
	}
	err := s.FinishNode(ctx, "run-full", "last", "success", "", []byte(`1`))
	if !errors.Is(err, store.ErrOutputLimit) || !errors.Is(err, store.ErrOutputNotStored) {
		t.Fatalf("a byte past the run's 1 GiB: err = %v, want the run limit", err)
	}
}

func TestReserveUpload_ASlotlessTeamStoresOnlySmallOutputs(t *testing.T) {
	s := storetest.Open(t)
	teamHandle(t, s, "team-slotless")
	if err := s.SetFreeTeamSlots(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	reserve := func(size int64) error {
		key, _ := store.NewOutputKey("run-slotless", "n", 1, 0)
		_, err := s.ReserveUpload(t.Context(), store.UploadRequest{
			Team: "team-slotless", RunID: "run-slotless", Kind: store.StorageCache, Key: key, Size: size,
			SHA256: strings.Repeat("a", 64), Principal: "p", Provenance: "cloud",
		})
		return err
	}
	key, _ := store.NewOutputKey("run-slotless", "n", 1, 0)
	u, err := s.ReserveUpload(t.Context(), store.UploadRequest{
		Team: "team-slotless", RunID: "run-slotless", Kind: store.StorageCache, Key: key, Size: store.MaxUnpaidOutputBytes,
		SHA256: strings.Repeat("a", 64), Principal: "p", Provenance: "cloud",
	})
	if err != nil {
		t.Fatalf("a 1 MiB output: %v", err)
	}
	if err := s.CommitUpload(t.Context(), "team-slotless", u.ID, "p", time.Now()); err != nil {
		t.Fatalf("committing a 1 MiB output with every slot taken: %v", err)
	}
	if err := reserve(store.MaxUnpaidOutputBytes + 1); !errors.Is(err, store.ErrFreeStoragePaused) {
		t.Fatalf("an output past 1 MiB: err = %v, want free storage paused", err)
	}
	if _, err := s.ReserveUpload(t.Context(), store.UploadRequest{
		Team: "team-slotless", RunID: "run-slotless", Kind: store.StorageCache, Key: "artifacts/blobs/" + strings.Repeat("b", 64),
		Size: 1, SHA256: strings.Repeat("b", 64), Principal: "p", Provenance: "cloud",
	}); !errors.Is(err, store.ErrFreeStoragePaused) {
		t.Fatalf("a one-byte artifact: err = %v, want free storage paused", err)
	}
}

func TestReserveUpload_AFullShareStillTakesSmallOutputs(t *testing.T) {
	s := storetest.Open(t)
	setFreeAllowance(t, s, 16<<10)
	freeTeam(t, s, "team-full")
	reserve := func(key string, size int64) error {
		_, err := s.ReserveUpload(t.Context(), store.UploadRequest{
			Team: "team-full", RunID: "run-full", Kind: store.StorageCache, Key: key, Size: size,
			SHA256: strings.Repeat("a", 64), Principal: "p", Provenance: "cloud",
		})
		return err
	}
	output := func() string {
		key, _ := store.NewOutputKey("run-full", "n", 1, 0)
		return key
	}
	if err := reserve("artifacts/blobs/"+strings.Repeat("c", 64), 12<<10); err != nil {
		t.Fatalf("filling the share: %v", err)
	}
	if err := reserve(output(), store.MaxUnpaidOutputBytes); err != nil {
		t.Fatalf("a 1 MiB output past a full share: %v", err)
	}
	if got := usageOf(t, s, "team-full", store.StorageCache).ReservedBytes; got < store.MaxUnpaidOutputBytes {
		t.Fatalf("reserved bytes = %d, want the small output counted", got)
	}
	if err := reserve(output(), store.MaxUnpaidOutputBytes+1); !errors.Is(err, store.ErrStorageQuota) {
		t.Fatalf("an output past 1 MiB with a full share: err = %v, want the storage refusal", err)
	}
	if err := reserve("artifacts/blobs/"+strings.Repeat("d", 64), 1); !errors.Is(err, store.ErrStorageQuota) {
		t.Fatalf("a one-byte artifact with a full share: err = %v, want the storage refusal", err)
	}
}

func TestWriteOutputFileDurably_KeepsAMatchingFileAndReplacesAnother(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(path, []byte(`"same"`), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	if err := store.WriteOutputFileDurably(path, []byte(`"same"`)); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(path); !os.SameFile(before, after) {
		t.Fatal("a file that already matched was replaced instead of synced in place")
	}
	if err := store.WriteOutputFileDurably(path, []byte(`"other"`)); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != `"other"` {
		t.Fatalf("a mismatched file = %s, want it replaced", got)
	}
}

func TestReserveUpload_SmallOutputsStopAtTheOverage(t *testing.T) {
	s := storetest.Open(t)
	teamHandle(t, s, "team-over")
	if err := s.SetFreeTeamSlots(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	reserve := func(size int64) error {
		key, _ := store.NewOutputKey("run-over", "n", 1, 0)
		_, err := s.ReserveUpload(t.Context(), store.UploadRequest{
			Team: "team-over", RunID: "run-over", Kind: store.StorageCache, Key: key, Size: size,
			SHA256: strings.Repeat("a", 64), Principal: "p", Provenance: "cloud",
		})
		return err
	}
	for i := range store.MaxSmallOutputOverage / store.MaxUnpaidOutputBytes {
		if err := reserve(store.MaxUnpaidOutputBytes); err != nil {
			t.Fatalf("small output %d within the 64 MiB overage: %v", i, err)
		}
	}
	if err := reserve(1); !errors.Is(err, store.ErrFreeStoragePaused) {
		t.Fatalf("a small output past the overage: err = %v, want a storage refusal", err)
	}
}

// A read of an output whose file was changed or lost is an error a cache hit
// can recognize, never the wrong bytes.
func TestGetNodeOutput_ReportsBadOrMissingBytes(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	if err := s.CreateRun(ctx, store.Run{ID: "run-a", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-a", NodeID: "n", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishNode(ctx, "run-a", "n", "success", "", []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	n, _ := s.GetNode(ctx, "run-a", "n")
	path := store.OutputPath(s.OutputDir(), n.OutputRef.Key)
	if err := os.WriteFile(path, []byte(`{"v":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := s.GetNodeOutput(ctx, "run-a", "n"); !errors.Is(err, store.ErrOutputCorrupt) {
		t.Fatalf("a changed output file = %s, %v; want it refused as corrupt", out, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if out, err := s.GetNodeOutput(ctx, "run-a", "n"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a lost output file = %s, %v; want not found", out, err)
	}
}
