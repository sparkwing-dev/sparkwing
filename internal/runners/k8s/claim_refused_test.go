package k8s

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// The spend defect: a team with no credits reached the fallback, the controller
// answered the named claim 402, and the Job was created anyway, unfenced and
// uncharged. A refused claim now creates no Job and fails the node.
func TestRunNode_RefusedClaimCreatesNoJob(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		wantFailed bool
		wantReason string
	}{
		{
			name: "insufficient credits", status: http.StatusPaymentRequired,
			body:       `{"error":"insufficient credits: balance 0, need 60"}`,
			wantFailed: true, wantReason: store.FailureCreditsExhausted,
		},
		{
			name: "server error", status: http.StatusInternalServerError,
			body: `{"error":"boom"}`, wantFailed: true, wantReason: store.FailureUnknown,
		},
		{name: "another holder", status: http.StatusForbidden, body: `{"error":"held"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
				t.Fatalf("CreateRun: %v", err)
			}
			if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "pending"}); err != nil {
				t.Fatalf("CreateNode: %v", err)
			}
			controllerHandler := controller.New(st, nil).Handler()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/nodes/build/claim") {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
					return
				}
				controllerHandler.ServeHTTP(w, r)
			}))
			defer srv.Close()

			var jobs atomic.Int32
			kcli := fake.NewSimpleClientset()
			kcli.PrependReactor("create", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
				jobs.Add(1)
				return false, nil, nil
			})
			r := New(kcli, client.New(srv.URL, nil), Config{
				Namespace: "default", Image: "runner", ControllerURL: srv.URL,
				PollInterval: time.Millisecond, MissingJobGracePeriod: time.Millisecond,
			}, nil)

			// safety: a Job that was created waits on a pod the fake never runs, so the
			// bound turns a regression into a failure rather than a hung test.
			runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			res := r.RunNode(runCtx, runner.Request{RunID: "run-1", NodeID: "build"})
			if n := jobs.Load(); n != 0 {
				t.Fatalf("created %d Jobs for a node whose claim was refused, want none", n)
			}
			if res.Outcome != sparkwing.Failed || res.Err == nil {
				t.Fatalf("result = %+v, want a failure naming the refusal", res)
			}
			node, err := st.GetNode(ctx, "run-1", "build")
			if err != nil {
				t.Fatalf("GetNode: %v", err)
			}
			if failed := node.Outcome == string(sparkwing.Failed); failed != tc.wantFailed {
				t.Fatalf("node outcome = %q, want failed=%v", node.Outcome, tc.wantFailed)
			}
			if tc.wantFailed && node.FailureReason != tc.wantReason {
				t.Errorf("failure reason = %q, want %q", node.FailureReason, tc.wantReason)
			}
		})
	}
}
