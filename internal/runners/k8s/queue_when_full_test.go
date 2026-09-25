package k8s

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

var queueTestJob = JobName("run-1", "build", 0)

// safety: every wait here is budgeted in polls rather than in seconds, so the
// grace period is the smallest value that still fires on the second
// unschedulable poll. A run that survives more polls than that can only have
// taken the queue.
const queueTestGrace = time.Nanosecond

type activityWatch struct {
	inner http.Handler
	want  string
	once  sync.Once
	seen  chan struct{}
}

func watchActivity(want string) *activityWatch {
	return &activityWatch{want: want, seen: make(chan struct{})}
}

func (a *activityWatch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/activity") && r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			if bytes.Contains(body, []byte(a.want)) {
				a.once.Do(func() { close(a.seen) })
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
	}
	a.inner.ServeHTTP(w, r)
}

func queueTestStore(t *testing.T, watch *activityWatch) (*store.Store, *httptest.Server) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "running"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	h := controller.New(st, nil).Handler()
	if watch != nil {
		watch.inner = h
		h = watch
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return st, srv
}

func pendingJob() *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: queueTestJob, Namespace: "default"},
		Status:     batchv1.JobStatus{Active: 1},
	}
}

func completedJob() *batchv1.Job {
	j := pendingJob()
	j.Status = batchv1.JobStatus{Succeeded: 1, Conditions: []batchv1.JobCondition{
		{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
	}}
	return j
}

func unschedulablePodAsking(cores int64, band string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-1",
			Namespace: "default",
			Labels:    map[string]string{"batch.kubernetes.io/job-name": queueTestJob},
		},
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{cpuBandKey: band},
			Containers: []corev1.Container{{
				Name: "runner",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    *resource.NewQuantity(cores, resource.DecimalSI),
					corev1.ResourceMemory: *resource.NewQuantity(store.CPUClassMemoryBytes(cores), resource.BinarySI),
				}},
			}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
				Reason:  corev1.PodReasonUnschedulable,
				Message: "0/3 nodes are available: 3 Insufficient cpu.",
			}},
		},
	}
}

func poolNode(name, band string, cores int64) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{cpuBandKey: band}},
		Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewQuantity(cores, resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(store.CPUClassMemoryBytes(cores), resource.BinarySI),
		}},
	}
}

func TestRunNode_ImpossibleRequestFailsBeforeCreatingJob(t *testing.T) {
	ctx := context.Background()
	_, srv := queueTestStore(t, nil)
	node := poolNode("small", "", 1)
	node.Status.Allocatable[corev1.ResourceCPU] = resource.MustParse("250m")
	kcli := fake.NewSimpleClientset(node)
	r := New(kcli, client.New(srv.URL, nil), Config{
		Namespace: "default", Image: "runner", ControllerURL: srv.URL,
		CPURequest: "100m", MemoryRequest: "128Mi",
	}, nil)
	pin := (&sparkwing.JobNode{}).Resources(sparkwing.Cores(1))
	result := r.RunNode(ctx, runner.Request{RunID: "run-1", NodeID: "build", Node: pin})
	if result.Outcome != sparkwing.Failed || result.Err == nil ||
		!strings.Contains(result.Err.Error(), "allocatable") {
		t.Fatalf("result = %+v, want immediate allocatable-capacity failure", result)
	}
	jobs, err := kcli.BatchV1().Jobs("default").List(ctx, metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 0 {
		t.Fatalf("jobs = %+v, err = %v, want none", jobs, err)
	}
}

// A pod the pool could hold if one of its machines were empty is waiting on
// occupancy, so it queues and says so, and it runs once a machine frees. The
// pod stays unschedulable for more polls than the grace period covers, so a
// run that reaches a machine at all can only have queued for it.
func TestRunNode_FleetFullQueuesUntilAMachineFrees(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	watch := watchActivity("queued: the runner fleet is full")
	st, srv := queueTestStore(t, watch)

	const pollsHeldInQueue = 4
	pod := unschedulablePodAsking(8, cpuBandSmall)
	kcli := fake.NewSimpleClientset(pendingJob(), pod, poolNode("pool-a", cpuBandSmall, 8))

	var mu sync.Mutex
	var freeOnce sync.Once
	polls := 0
	kcli.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		polls++
		held := polls <= pollsHeldInQueue
		mu.Unlock()
		if held {
			return true, &corev1.PodList{Items: []corev1.Pod{*pod}}, nil
		}
		freeOnce.Do(func() {
			if err := st.FinishNode(ctx, "run-1", "build",
				string(sparkwing.Success), "", []byte(`{"ok":true}`)); err != nil {
				t.Errorf("FinishNode: %v", err)
			}
		})
		running := pod.DeepCopy()
		running.Status.Phase = corev1.PodRunning
		running.Status.Conditions = nil
		return true, &corev1.PodList{Items: []corev1.Pod{*running}}, nil
	})
	kcli.PrependReactor("get", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		if polls > pollsHeldInQueue {
			return true, completedJob(), nil
		}
		return true, pendingJob(), nil
	})

	r := New(kcli, client.New(srv.URL, nil), Config{
		Namespace: "default", Image: "runner", ControllerURL: srv.URL,
		PollInterval:             time.Millisecond,
		MissingJobGracePeriod:    time.Millisecond,
		UnschedulableGracePeriod: queueTestGrace,
		FleetFullWait:            time.Hour,
	}, nil)

	res := r.RunNode(ctx, runner.Request{RunID: "run-1", NodeID: "build"})
	if res.Outcome != sparkwing.Success {
		t.Fatalf("outcome = %q (err %v), want success: the pod queued and then got a machine",
			res.Outcome, res.Err)
	}
	select {
	case <-watch.seen:
	default:
		t.Fatal("no queued status_detail reached the controller, so nothing told the dashboard why it waited")
	}

	events, err := st.ListEventsAfter(ctx, "run-1", 0, 100)
	if err != nil {
		t.Fatalf("ListEventsAfter: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Kind == eventQueuedOnFleet {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %s event, so the run's timeline does not record the wait", eventQueuedOnFleet)
	}
}

// Regression guard: a pod larger than any machine the pool runs cannot be
// cured by waiting, so it keeps the grace-period failure it has always had and
// never enters the queue. The queue budget is an hour, so a run that queued by
// mistake ends at the test's own deadline rather than passing quietly.
func TestRunNode_ShapeNoMachineCanHoldStillFailsAtTheGracePeriod(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, srv := queueTestStore(t, nil)

	pod := unschedulablePodAsking(64, cpuBandSmall)
	kcli := fake.NewSimpleClientset(pendingJob(), pod,
		poolNode("pool-a", cpuBandSmall, 8), poolNode("pool-b", cpuBandSmall, 8))

	r := New(kcli, client.New(srv.URL, nil), Config{
		Namespace: "default", Image: "runner", ControllerURL: srv.URL,
		PollInterval:             time.Millisecond,
		MissingJobGracePeriod:    time.Millisecond,
		UnschedulableGracePeriod: queueTestGrace,
		FleetFullWait:            time.Hour,
	}, nil)

	res := r.RunNode(ctx, runner.Request{RunID: "run-1", NodeID: "build"})
	if res.Outcome != sparkwing.Failed {
		t.Fatalf("outcome = %q, want failed at the grace period", res.Outcome)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "no node accepted this pod within") {
		t.Fatalf("err = %v, want the unschedulable failure, not the queue timeout", res.Err)
	}
	n, err := st.GetNode(ctx, "run-1", "build")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.FailureReason != store.FailureUnknown {
		t.Fatalf("failure_reason = %q, want the unchanged %q", n.FailureReason, store.FailureUnknown)
	}
	if strings.HasPrefix(n.StatusDetail, "queued:") {
		t.Fatalf("status_detail = %q: a pod no machine can hold must never read as queued", n.StatusDetail)
	}
}

// The pool running nothing is not a full pool, so a pod waiting on a machine
// that has yet to be launched keeps the grace period rather than the queue.
func TestFleetRunsAShapeFor(t *testing.T) {
	pod := unschedulablePodAsking(8, cpuBandSmall)
	for _, tc := range []struct {
		name  string
		nodes []runtime.Object
		want  bool
	}{
		{name: "pool runs nothing", want: false},
		{
			name:  "pool machine would hold it when empty",
			nodes: []runtime.Object{poolNode("pool-a", cpuBandSmall, 8)},
			want:  true,
		},
		{
			name:  "every pool machine is too small",
			nodes: []runtime.Object{poolNode("pool-a", cpuBandSmall, 4)},
			want:  false,
		},
		{
			name:  "the only machine big enough carries another band's label",
			nodes: []runtime.Object{poolNode("pool-a", "other-band", 64)},
			want:  false,
		},
		{
			name:  "a cordoned machine does not count",
			nodes: []runtime.Object{cordoned(poolNode("pool-a", cpuBandSmall, 8))},
			want:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := New(fake.NewSimpleClientset(tc.nodes...), nil, Config{Namespace: "default"}, nil)
			if got := r.fleetRunsAShapeFor(context.Background(), pod); got != tc.want {
				t.Fatalf("fleetRunsAShapeFor = %v, want %v", got, tc.want)
			}
		})
	}
}

func cordoned(n *corev1.Node) *corev1.Node {
	n.Spec.Unschedulable = true
	return n
}
