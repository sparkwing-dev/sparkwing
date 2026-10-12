package controller

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

// A runner's node claim token uploads its node's log segments, filed under
// the claim's attempt. The runner seals the log after its node reports, so
// the ended claim still finishes that attempt's log, and opens no other.
func TestNodeClaimTokenUploadsItsNodesLogSegmentsUntilTheClaimEnds(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	now := time.Now()
	runner, _, err := st.CreateToken("pool", store.TokenKindRunner,
		[]string{ScopeNodesClaim, ScopeRunsState}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(st, nil).EnableAuthFromStore()
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "p", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "n1", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "r1", "n1"); err != nil {
		t.Fatal(err)
	}
	n, err := client.NewWithToken(hs.URL, nil, runner).ClaimNode(ctx, "pool:1", nil, time.Minute, nil)
	if err != nil || n == nil || n.ClaimToken == "" {
		t.Fatalf("claim node: %+v, %v", n, err)
	}
	node := client.NewWithToken(hs.URL, nil, n.ClaimToken)
	up := node.NewLogSegmentUploader(ctx, "r1", "n1")
	if err := up.Open(ctx); err != nil {
		t.Fatalf("open the log with the claim token: %v", err)
	}
	sink, err := logseg.NewSink(ctx, up, logseg.Options{
		Run: "r1", Node: "n1", SpoolDir: t.TempDir(), SegmentBytes: 16,
		RetryBase: time.Millisecond, RetryMax: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Append([]byte("step 1\n"), "", 0, 0); err != nil {
		t.Fatal(err)
	}
	fence := store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	}
	if err := node.FinishNode(store.WithNodeClaimFence(ctx, fence), "r1", "n1", "success", "", nil); err != nil {
		t.Fatalf("finish with the claim token: %v", err)
	}
	if err := sink.Append([]byte("step 2\n"), "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if m, err := sink.Close(ctx, logseg.End{}); err != nil || m.Status != logseg.StatusComplete {
		t.Fatalf("seal after the node reported = %+v, %v", m, err)
	}
	got, err := srv.dashboardLogs(store.DefaultTeam).Read(ctx, "r1", "n1", storage.ReadOpts{})
	if err != nil || string(got) != "step 1\nstep 2\n" {
		t.Fatalf("read %q, %v", got, err)
	}
	if err := node.NewLogSegmentUploader(ctx, "r1", "n1").Open(ctx); err == nil {
		t.Fatal("the claim token opened the node's log after its claim ended")
	}
}
