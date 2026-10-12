package controller_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type crossRunFixture struct {
	runnerClaimFixture
	runnerClient *client.Client
}

// hack: globex's lib run is newer than the claim team's, so a latest-run
// lookup that crossed teams would answer with it.
func newCrossRunFixture(t *testing.T) crossRunFixture {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	now := time.Now()
	scopes := []string{controller.ScopeNodesClaim, controller.ScopeTriggersClaim, controller.ScopeRunsState}
	runner, _, err := st.CreateToken("runner-a", store.TokenKindRunner, scopes, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AsOperator().CreateTeam(ctx, "globex"); err != nil {
		t.Fatal(err)
	}
	globex, err := st.ForTeam(ctx, "globex")
	if err != nil {
		t.Fatal(err)
	}
	globexRunner, _, err := globex.CreateToken(ctx, "runner-g", store.TokenKindRunner, scopes, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)

	if err := st.CreateRun(ctx, store.Run{ID: "lib-1", Pipeline: "lib", Status: "running", StartedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "lib-1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNode(ctx, "lib-1", "build", "success", "", []byte(`{"from":"own-team"}`)); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(ctx, "lib-1", "success", ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"g-lib", "g-parent"} {
		if err := globex.CreateRun(ctx, store.Run{ID: id, Pipeline: strings.TrimPrefix(id, "g-"), Status: "running", StartedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "g-lib", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := globex.MarkNodeReady(ctx, "g-lib", "build"); err != nil {
		t.Fatal(err)
	}
	gn, err := client.NewWithToken(srv.URL, nil, globexRunner).ClaimNode(ctx, "runner:g:1", nil, time.Minute, nil)
	if err != nil || gn == nil {
		t.Fatalf("globex claim = %+v, %v", gn, err)
	}
	if err := client.NewWithToken(srv.URL, nil, gn.ClaimToken).FinishNode(ctx, "g-lib", "build", "success", "", nil); err != nil {
		t.Fatalf("finish globex's lib node: %v", err)
	}
	if err := globex.FinishRun(ctx, "g-lib", "success", ""); err != nil {
		t.Fatal(err)
	}

	if err := st.CreateTrigger(ctx, store.Trigger{ID: "parent", Pipeline: "deploy", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	seedRunNode(t, st, "parent", "ship")
	if err := st.MarkNodeReady(ctx, "parent", "ship"); err != nil {
		t.Fatal(err)
	}
	return crossRunFixture{
		runnerClaimFixture: runnerClaimFixture{st: st, url: srv.URL, runner: runner},
		runnerClient:       client.NewWithToken(srv.URL, nil, runner),
	}
}

func (f crossRunFixture) claim(t *testing.T, run string) string {
	t.Helper()
	n, err := f.runnerClient.ClaimNode(context.Background(), "runner:box-a:1", nil, time.Minute, nil)
	if err != nil || n == nil || n.RunID != run || n.ClaimToken == "" {
		t.Fatalf("claim = %+v, %v; want a claim on %s", n, err, run)
	}
	return n.ClaimToken
}

func refused(err error) bool { return err != nil && strings.Contains(err.Error(), "403") }

// D-SEC-08: a node's Ref.Get reads the latest run of a pipeline in its own
// team and that run's outputs under the node's claim token, and nothing of
// another team's.
func TestRunnerClaim_NodeTokenReadsItsTeamsPipelineRefs(t *testing.T) {
	f := newCrossRunFixture(t)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, f.claim(t, "parent"))
	run, err := c.GetLatestRun(ctx, "lib", []string{"success"}, 0)
	if err != nil || run == nil || run.ID != "lib-1" {
		t.Fatalf("latest lib run = %+v, %v; want the claim's team's lib-1", run, err)
	}
	out, err := c.GetNodeOutput(ctx, "lib-1", "build")
	if err != nil || !strings.Contains(string(out), "own-team") {
		t.Fatalf("lib-1's output = %s, %v", out, err)
	}
	if _, err := c.GetLatestRun(ctx, "parent", []string{"success"}, 0); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("latest of a pipeline only another team runs: %v, want not found", err)
	}
	if out, err := c.GetNodeOutput(ctx, "g-lib", "build"); err == nil {
		t.Errorf("another team's output = %s, want refused", out)
	}
	for _, c2 := range []struct{ method, path string }{
		{"GET", "/api/v1/runs/lib-1"},
		{"GET", "/api/v1/runs/lib-1/nodes"},
		{"GET", "/api/v1/runs/lib-1/nodes/build"},
		{"GET", "/api/v1/runs"},
		{"GET", "/api/v1/pipelines/lib/profile"},
		{"GET", "/api/v1/triggers"},
	} {
		if got, body := f.do(t, c2.method, c2.path, c.Token(), ""); got != http.StatusForbidden || body["error"] != "claim_mismatch" {
			t.Errorf("%s %s = %d %v, want 403 claim_mismatch", c2.method, c2.path, got, body)
		}
	}
}

// D-SEC-08: a node's RunAndAwait starts a child run in its own team recorded
// with the node as its parent, polls that child, and finds it again from a
// retry of the node. The child gets claims of its own, and the token starts
// nothing that is not its node's child.
func TestRunnerClaim_NodeTokenStartsAndAwaitsItsOwnChildRuns(t *testing.T) {
	f := newCrossRunFixture(t)
	ctx := context.Background()
	tok := f.claim(t, "parent")
	c := client.NewWithToken(f.url, nil, tok)
	child := func(parentRun, parentNode string) (*client.TriggerResponse, error) {
		return c.CreateTrigger(ctx, client.TriggerRequest{
			Pipeline: "lib", Trigger: client.TriggerMeta{Source: "await-pipeline"},
			ParentRunID: parentRun, ParentNodeID: parentNode,
		})
	}
	resp, err := child("parent", "ship")
	if err != nil || resp == nil || resp.RunID == "" {
		t.Fatalf("a child of the claim's own node = %+v, %v", resp, err)
	}
	trig, err := f.st.GetTrigger(ctx, resp.RunID)
	if err != nil || trig.ParentRunID != "parent" || trig.ParentNodeID != "ship" {
		t.Fatalf("child trigger = %+v, %v; want parent/ship as its parent", trig, err)
	}
	for _, p := range [][2]string{{"g-parent", "ship"}, {"parent", "other"}, {"", ""}, {"lib-1", "build"}} {
		if _, err := child(p[0], p[1]); !refused(err) {
			t.Errorf("a trigger naming parent %q/%q: %v, want 403", p[0], p[1], err)
		}
	}
	if run, err := c.GetRun(ctx, resp.RunID); err != nil || run.ID != resp.RunID {
		t.Fatalf("polling the child run = %+v, %v", run, err)
	}
	if got, body := f.do(t, "GET", "/api/v1/runs/"+resp.RunID+"?include="+store.IncludeSecretValues, tok, ""); got != http.StatusForbidden {
		t.Errorf("the child's secret values = %d %v, want 403", got, body)
	}
	if id, err := c.FindSpawnedChildTriggerID(ctx, "parent", "ship", "lib"); err != nil || id != resp.RunID {
		t.Fatalf("spawned child lookup = %q, %v; want %s", id, err, resp.RunID)
	}
	for _, p := range [][2]string{{"g-parent", "ship"}, {"parent", "other"}, {"lib-1", "ship"}} {
		if _, err := c.FindSpawnedChildTriggerID(ctx, p[0], p[1], "lib"); !refused(err) {
			t.Errorf("spawned child of %q/%q: %v, want 403", p[0], p[1], err)
		}
	}
	claimed, err := f.runnerClient.ClaimSpecificTrigger(ctx, resp.RunID, time.Minute)
	if err != nil || claimed == nil || claimed.ClaimToken == "" || claimed.ClaimToken == tok {
		t.Fatalf("the child's own trigger claim = %+v, %v; want a token of its own", claimed, err)
	}

	if got, body := f.do(t, "POST", "/api/v1/runs/parent/nodes/ship/finish", tok, `{"outcome":"success"}`); got/100 != 2 {
		t.Fatalf("finish the parent node = %d %v", got, body)
	}
	if _, err := c.FindSpawnedChildTriggerID(ctx, "parent", "ship", "lib"); !refused(err) {
		t.Errorf("a spawned child lookup after the claim ended: %v, want 403", err)
	}
	if _, err := c.GetLatestRun(ctx, "lib", nil, 0); !refused(err) {
		t.Errorf("a latest-run read after the claim ended: %v, want 403", err)
	}
	now := time.Now()
	if err := f.st.CreateTrigger(ctx, store.Trigger{ID: "parent-2", Pipeline: "deploy", RetryOf: "parent", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.CreateRun(ctx, store.Run{ID: "parent-2", Pipeline: "deploy", Status: "running", RetryOf: "parent", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.CreateNode(ctx, store.Node{RunID: "parent-2", NodeID: "ship", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.MarkNodeReady(ctx, "parent-2", "ship"); err != nil {
		t.Fatal(err)
	}
	retry := client.NewWithToken(f.url, nil, f.claim(t, "parent-2"))
	if id, err := retry.FindSpawnedChildTriggerID(ctx, "parent", "ship", "lib"); err != nil || id != resp.RunID {
		t.Fatalf("a retry's lookup of its source attempt's child = %q, %v; want %s", id, err, resp.RunID)
	}
	retried := func(of string) (*client.TriggerResponse, error) {
		return retry.CreateTrigger(ctx, client.TriggerRequest{
			Pipeline: "lib", Trigger: client.TriggerMeta{Source: "await-pipeline"},
			ParentRunID: "parent-2", ParentNodeID: "ship", RetryOf: of,
		})
	}
	if _, err := retried("lib-1"); !refused(err) {
		t.Errorf("a child retrying a run no attempt of the node started: %v, want 403", err)
	}
	if again, err := retried(resp.RunID); err != nil || again.RunID == "" {
		t.Fatalf("a child retrying the source attempt's child = %+v, %v", again, err)
	}
}
