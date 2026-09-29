package store_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func commitOutput(ctx context.Context, t *testing.T, s *store.Store, team store.Team, runID, nodeID string, data []byte) store.OutputRef {
	t.Helper()
	key, err := store.NewOutputKey(runID, nodeID)
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
	own := commitOutput(t.Context(), t, f.s, store.DefaultTeam, f.run, "a", []byte(`{"v":1}`))
	other := commitOutput(t.Context(), t, f.s, store.DefaultTeam, f.run, "b", []byte(`{"v":2}`))
	uncommitted, _ := store.NewOutputKey(f.run, "a")
	tok := f.claim(t, "a", store.ClaimTokenWork)

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
}

func TestReserveUpload_EnforcesOutputLimits(t *testing.T) {
	s := storetest.Open(t)
	req := func(run string, size int64) error {
		key, _ := store.NewOutputKey(run, "n")
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
	key, _ := store.NewOutputKey("run-other", "n")
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
		key, _ := store.NewOutputKey("run-share", "n")
		_, err := s.ReserveUpload(t.Context(), store.UploadRequest{
			Team: "team-out", RunID: "run-share", Kind: store.StorageCache, Key: key, Size: size,
			SHA256: strings.Repeat("a", 64), Principal: "p", Provenance: "cloud",
		})
		return err
	}
	if err := reserve(1 << 10); err != nil {
		t.Fatalf("an output within the share: %v", err)
	}
	if err := reserve(64 << 10); !errors.Is(err, store.ErrStorageQuota) {
		t.Fatalf("an output past the share: err = %v, want a storage refusal", err)
	}
}

func TestExpiredOutputRuns_KeepsTheNewestSuccessPerPipeline(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	old := time.Now().Add(-store.OutputRetention - time.Hour)
	mk := func(id, pipeline, status string, finished time.Time) {
		t.Helper()
		if err := s.CreateRun(ctx, store.Run{ID: id, Pipeline: pipeline, Status: "running", StartedAt: finished}); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateNode(ctx, store.Node{RunID: id, NodeID: "n", Status: "running"}); err != nil {
			t.Fatal(err)
		}
		commitOutput(ctx, t, s, store.DefaultTeam, id, "n", []byte(`"`+id+`"`))
		if err := store.FinishRunAtForTest(ctx, s, id, status, finished); err != nil {
			t.Fatal(err)
		}
	}
	mk("run-old-success", "p", "success", old.Add(-time.Hour))
	mk("run-newest-success", "p", "success", old)
	mk("run-old-failed", "p", "failed", old)
	mk("run-recent-failed", "p", "failed", time.Now().Add(-time.Hour))
	mk("run-other-pipeline", "q", "success", old)

	expired, err := s.ExpiredOutputRuns(ctx, time.Now(), 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range expired {
		got[e.RunID] = true
	}
	for _, want := range []string{"run-old-success", "run-old-failed"} {
		if !got[want] {
			t.Errorf("%s is past retention but not listed: %v", want, got)
		}
	}
	for _, keep := range []string{"run-newest-success", "run-recent-failed", "run-other-pipeline"} {
		if got[keep] {
			t.Errorf("%s is listed for deletion: %v", keep, got)
		}
	}
	if err := s.DeleteRunOutputs(ctx, store.DefaultTeam, "run-old-success"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NodeOutputObject(ctx, store.DefaultTeam, "run-old-success", "n"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a deleted run's output still resolves: %v", err)
	}
	if n, err := s.PruneExpiredCacheObjects(ctx, time.Now().Add(store.DirectCacheMaxAge+time.Hour)); err != nil || n != 0 {
		t.Fatalf("the cache age rule deleted %d output rows: %v", n, err)
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
