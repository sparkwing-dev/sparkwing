package controller_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const specA = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func (f *appFixture) launchNode(runID, nodeID string) string {
	f.t.Helper()
	c, err := f.store.ClaimLaunch(store.WithoutCreditMetering(context.Background()),
		store.ClaimIdentity{Principal: "launcher", TokenPrefix: "swr_launch"},
		store.LaunchClaimRequest{HolderID: "launcher:" + runID, Lease: time.Minute, Deadline: time.Hour, RunID: runID, NodeID: nodeID},
		time.Now())
	if err != nil || c == nil {
		f.t.Fatalf("launch claim %s/%s: %+v %v", runID, nodeID, c, err)
	}
	return c.Token
}

// A pod's claim token reads its own run, beats its own claim, and starts its
// execution once; after that the claim gets no source credential, and a cancel
// reaches the pod through the beat while its reads are refused.
func TestClaimRun_PodRoutesServeOnlyTheirOwnClaim(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	plan := "Bearer " + f.launchedRun(olga, "run-pod", "acme", "widgets")
	f.launchedRun(olga, "run-next", "acme", "widgets")

	var run store.Run
	if code := f.call("GET", "/api/v1/runs/run-pod?include=secret_values", plan, nil, &run); code != http.StatusOK || run.Pipeline != "build" {
		t.Fatalf("own run = %d %+v", code, run)
	}
	for _, path := range []string{"/api/v1/runs/run-next", "/api/v1/runs/run-next/nodes/plan/heartbeat", "/api/v1/runs/run-pod/nodes/other/heartbeat"} {
		method := "POST"
		if !strings.HasSuffix(path, "heartbeat") {
			method = "GET"
		}
		if code := f.call(method, path, plan, map[string]int{}, nil); code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", method, path, code)
		}
	}
	var beat struct{ Cancel bool }
	if code := f.call("POST", "/api/v1/runs/run-pod/nodes/plan/heartbeat", plan, map[string]int{"lease_secs": 120}, &beat); code != http.StatusOK || beat.Cancel {
		t.Fatalf("beat = %d %+v", code, beat)
	}
	if code := f.call("POST", "/api/v1/runs/run-pod/nodes/plan/execution-start", plan, nil, nil); code != http.StatusOK {
		t.Fatalf("execution start = %d", code)
	}
	var refused map[string]any
	if code := f.call("POST", "/api/v1/runs/run-pod/source-credential", plan, nil, &refused); code != http.StatusForbidden ||
		refused["error"] != "source_credential_spent" {
		t.Fatalf("source credential after the start = %d %v, want 403 source_credential_spent", code, refused)
	}
	for _, path := range []string{"/api/v1/launcher/sync", "/api/v1/runs/run-pod/children"} {
		if code := f.call("POST", path, plan, map[string]any{}, nil); code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Errorf("POST %s with a plan claim = %d, want refused", path, code)
		}
	}
	if err := f.store.RequestCancel(context.Background(), "run-pod"); err != nil {
		t.Fatal(err)
	}
	if code := f.call("POST", "/api/v1/runs/run-pod/nodes/plan/heartbeat", plan, map[string]int{}, &beat); code != http.StatusOK || !beat.Cancel {
		t.Fatalf("beat after cancel = %d %+v, want the cancel", code, beat)
	}
	if code := f.call("GET", "/api/v1/runs/run-pod", plan, nil, nil); code != http.StatusForbidden {
		t.Fatalf("read after cancel = %d, want 403", code)
	}
	if code := f.call("POST", "/api/v1/runs/run-pod/nodes/plan/execution-start", plan, nil, nil); code != http.StatusForbidden {
		t.Fatalf("execution start after cancel = %d, want 403", code)
	}
}

// A node's child runs its parent's commit on the controller path, one child
// per call however often the call is retried, and only its parent reads it.
func TestClaimRun_ChildRunsInheritTheParentAndAnswerOnlyIt(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	plan := "Bearer " + f.launchedRun(olga, "run-parent", "acme", "widgets")
	doc := map[string]any{"nodes": []map[string]any{{"id": "a", "deps": []string{}, "spec_hash": specA}}}
	if code := f.call("POST", "/api/v1/runs/run-parent/plan", plan, doc, nil); code != http.StatusOK {
		t.Fatalf("accept plan = %d", code)
	}
	work := "Bearer " + f.launchNode("run-parent", "a")
	var out struct {
		RunID string `json:"run_id"`
	}
	if code := f.call("POST", "/api/v1/runs/run-parent/children", work, map[string]any{"ordinal": 0, "pipeline": "deploy"}, &out); code != http.StatusAccepted || out.RunID == "" {
		t.Fatalf("enqueue = %d %+v", code, out)
	}
	childID := out.RunID
	if code := f.call("POST", "/api/v1/runs/run-parent/children", work, map[string]any{"ordinal": 0, "pipeline": "deploy"}, &out); code != http.StatusAccepted || out.RunID != childID {
		t.Fatalf("retried enqueue = %d %s, want %s", code, out.RunID, childID)
	}
	child, err := f.store.GetTrigger(context.Background(), childID)
	if err != nil || child.GitSHA != headSHA || child.ParentRunID != "run-parent" || child.ParentNodeID != "a" {
		t.Fatalf("child trigger = %+v %v", child, err)
	}
	var repoID int64
	if err := f.store.DB().QueryRow(`SELECT github_repo_id FROM triggers WHERE id = ?`, childID).Scan(&repoID); err != nil || repoID != 701 {
		t.Fatalf("child repository id = %d %v, want the parent's 701", repoID, err)
	}
	if _, err := f.store.GetNode(context.Background(), childID, store.PlanNodeID); err != nil {
		t.Fatalf("the child of an opted-in repository has no planning node: %v", err)
	}
	for body, want := range map[string]int{
		`{"ordinal":1,"pipeline":"deploy","repo":"bob/tools"}`: http.StatusUnprocessableEntity,
		`{"ordinal":1,"pipeline":"build"}`:                     http.StatusConflict,
	} {
		var m map[string]any
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatal(err)
		}
		if code := f.call("POST", "/api/v1/runs/run-parent/children", work, m, nil); code != want {
			t.Errorf("enqueue %s = %d, want %d", body, code, want)
		}
	}
	var got store.Run
	if code := f.call("GET", "/api/v1/runs/run-parent/children/"+childID, work, nil, &got); code != http.StatusOK || got.ID != childID {
		t.Fatalf("read child = %d %+v", code, got)
	}
	f.launchedRun(olga, "run-stranger", "acme", "widgets")
	if code := f.call("GET", "/api/v1/runs/run-parent/children/run-stranger", work, nil, nil); code != http.StatusNotFound {
		t.Fatalf("read a run that is not a child = %d, want 404", code)
	}
	if code := f.call("GET", "/api/v1/runs/run-parent/children/"+childID, plan, nil, nil); code != http.StatusForbidden {
		t.Fatalf("read the child with the ended plan claim = %d, want 403", code)
	}
}
