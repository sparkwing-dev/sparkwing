package k8s

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/runners/warmpool"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// The live defect: no agent claimed the node, the warm dispatcher fell back to
// a Job, and every write the pod and the dispatcher made was refused with
// claim_required. The fallback now holds the claim, so a claim-scoped token
// carries the node to a terminal state and the Job carries the same fence.
func TestWarmFallback_JobHoldsTheClaimItExecutesUnder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	now := time.Now().UTC()
	// safety: the dispatcher's readiness routes bind to the run's trigger claim,
	// which a foreground test run has no trigger to hold.
	dispatcherToken, _, err := st.CreateToken("dispatcher", store.TokenKindRunner,
		[]string{controller.ScopeAdmin}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken dispatcher: %v", err)
	}
	poolToken, _, err := st.CreateToken("pool", store.TokenKindRunner, []string{
		controller.ScopeNodesClaim, controller.ScopeRunsRead, controller.ScopeRunsState,
	}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken pool: %v", err)
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(controller.New(st, quiet).EnableAuthFromStore().Handler())
	defer srv.Close()

	kcli := fake.NewSimpleClientset()
	created := make(chan *batchv1.Job, 1)
	kcli.PrependReactor("create", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		select {
		case created <- action.(k8stesting.CreateAction).GetObject().(*batchv1.Job):
		default:
		}
		return false, nil, nil
	})
	kcli.PrependReactor("get", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &batchv1.Job{Status: batchv1.JobStatus{Succeeded: 1}}, nil
	})
	fallback := New(kcli, client.NewWithToken(srv.URL, nil, poolToken), Config{
		Namespace: "default", Image: "runner", ControllerURL: srv.URL,
		PollInterval: time.Millisecond, MissingJobGracePeriod: time.Millisecond,
	}, quiet)
	warm := warmpool.New(client.NewWithToken(srv.URL, nil, dispatcherToken), fallback, warmpool.Config{
		PollInterval: time.Millisecond, ClaimWaitTimeout: 10 * time.Millisecond,
	}, quiet)

	warm.RunNode(ctx, runner.Request{RunID: "run-1", NodeID: "build"})

	n, err := st.GetNode(ctx, "run-1", "build")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	wantHolder := "k8s-job:" + JobName("run-1", "build", 0)
	if n.ClaimedBy != wantHolder {
		t.Fatalf("claimed_by = %q, want %q; the fallback executed a node it does not hold", n.ClaimedBy, wantHolder)
	}
	if n.Status != "done" {
		t.Fatalf("node status = %q, want done; the dispatcher's terminal write was refused", n.Status)
	}

	var job *batchv1.Job
	select {
	case job = <-created:
	default:
		t.Fatal("the fallback created no Job")
	}
	env := map[string]string{}
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	if env[ClaimHolderEnv] != wantHolder {
		t.Fatalf("%s = %q, want %q", ClaimHolderEnv, env[ClaimHolderEnv], wantHolder)
	}
	if env[ClaimGenerationEnv] != strconv.FormatInt(n.ClaimGeneration, 10) {
		t.Fatalf("%s = %q, want generation %d", ClaimGenerationEnv, env[ClaimGenerationEnv], n.ClaimGeneration)
	}
}
