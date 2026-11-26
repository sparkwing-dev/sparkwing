package launcher

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

type handoffControl struct {
	*controlFake
	deliveries chan *CloudJobAward
	prepare    func(context.Context, capability.LeaseRef)
	stallSync  atomic.Bool

	mu        sync.Mutex
	planEnded bool
	refused   map[capability.LeaseRef]bool
	prepared  map[capability.LeaseRef]int
}

func (c *handoffControl) waitClaim(ctx context.Context, _ string, _ []string, _ time.Duration) claimResult {
	select {
	case award := <-c.deliveries:
		return claimResult{award: award, immediate: true}
	case <-ctx.Done():
		return claimResult{err: ctx.Err()}
	}
}

func (c *handoffControl) Prepare(ctx context.Context, refs []capability.LeaseRef) (time.Time, error) {
	if c.prepare != nil {
		c.prepare(ctx, refs[0])
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prepared[refs[0]]++
	if c.refused[refs[0]] {
		return time.Time{}, &apierr.Error{Code: hosterr.LockHeld, Message: "award cancelled"}
	}
	return time.Now().Add(time.Minute), context.Cause(ctx)
}

func (c *handoffControl) Sync(ctx context.Context, observations []CloudJobObservation) ([]CloudJobState, error) {
	if c.stallSync.CompareAndSwap(true, false) {
		<-ctx.Done()
		return nil, context.Cause(ctx)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	states := make([]CloudJobState, len(observations))
	for i, observation := range observations {
		states[i] = CloudJobState{Binding: observation.Binding, LeaseExpiresAt: time.Now().Add(time.Hour), Delete: c.planEnded && observation.Binding.Kind == CloudPlan}
	}
	return states, nil
}

func (c *handoffControl) endPlan() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.planEnded = true
}

func (c *handoffControl) refuse(award *CloudJobAward) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refused[award.Binding.LeaseRef()] = true
}

func (c *handoffControl) preparations(award *CloudJobAward) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.prepared[award.Binding.LeaseRef()]
}

type handoffFixture struct {
	l       *Launcher
	kube    *fake.Clientset
	control *handoffControl
	plan    *corev1.Pod
	first   *CloudJobAward
	passes  atomic.Int32
}

func newHandoffFixture(t *testing.T) *handoffFixture {
	t.Helper()
	l, kube, _, base := fixture()
	boundary, nodes := boundaryFixture()
	node, err := nodes.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = kube.Tracker().Add(node); err != nil {
		t.Fatal(err)
	}
	boundary.Kube, l.Boundary = kube, boundary
	l.Config.MemoryCeiling = 16 << 30
	l.Poll = time.Minute
	control := &handoffControl{controlFake: base, deliveries: make(chan *CloudJobAward), refused: map[capability.LeaseRef]bool{}, prepared: map[capability.LeaseRef]int{}}
	l.Control = control
	f := &handoffFixture{l: l, kube: kube, control: control}
	kube.PrependReactor("list", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.(ktesting.ListAction).GetListRestrictions().Labels.Empty() {
			f.passes.Add(1)
		}
		return false, nil, nil
	})
	plan := *base.award
	plan.CPUCores, plan.Memory = 2, match.CloudSizeMemoryBytes(2)
	plan.Binding.Kind, plan.Binding.NodeID, plan.Binding.Attempt = CloudPlan, "", 0
	if _, err = l.launch(t.Context(), &plan); err != nil {
		t.Fatal(err)
	}
	job, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(plan.Binding), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f.plan = &corev1.Pod{ObjectMeta: *job.Spec.Template.ObjectMeta.DeepCopy(), Spec: *job.Spec.Template.Spec.DeepCopy()}
	f.plan.Name, f.plan.Namespace, f.plan.UID = "planning", "cloud", "planning-pod"
	f.plan.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr(true)}}
	f.plan.Status.Phase = corev1.PodRunning
	if _, err = kube.CoreV1().Pods("cloud").Create(t.Context(), f.plan, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	first := plan
	first.Binding.Kind, first.Binding.UnitID, first.Binding.NodeID, first.Binding.Attempt = CloudWork, "first", "first", 1
	f.first = &first
	return f
}

func (f *handoffFixture) run(t *testing.T) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- f.l.Run(ctx) }()
	synctest.Wait()
	return func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}

func (f *handoffFixture) job(t *testing.T, award *CloudJobAward) bool {
	t.Helper()
	_, err := f.kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(award.Binding), metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return err == nil
}

func (f *handoffFixture) deletePlanPod(t *testing.T) {
	t.Helper()
	if err := f.kube.CoreV1().Pods("cloud").Delete(t.Context(), f.plan.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
}

// hack: a second run needs its own fresh node, because a used node serves only the run that first used it.
func (f *handoffFixture) otherRun(t *testing.T) *CloudJobAward {
	t.Helper()
	node, err := f.kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	karpenter := f.l.Boundary.(*KubernetesBoundary).Karpenter.Resource(nodeClaims)
	claim, err := karpenter.Get(t.Context(), "claim", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	claim = claim.DeepCopy()
	claim.SetName("spare-claim")
	claim.SetUID("spare-claim-uid")
	claim.SetResourceVersion("")
	claim.Object["status"] = map[string]any{"nodeName": "spare", "providerID": "aws:///zone/i-spare"}
	if _, err = karpenter.Create(t.Context(), claim, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	owners := []metav1.OwnerReference{{APIVersion: "karpenter.sh/v1", Kind: "NodeClaim", Name: "spare-claim", UID: "spare-claim-uid"}}
	spare := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "spare", UID: "spare-uid", ResourceVersion: "1", OwnerReferences: owners, Labels: map[string]string{CloudClassLabel: "2", PoolNamespaceLabel: "cloud", WarmNamespaceLabel: "warm", NodeClassLabel: "sparkwing-jobs"}}, Spec: *node.Spec.DeepCopy(), Status: *node.Status.DeepCopy()}
	spare.Spec.Taints = []corev1.Taint{{Key: DedicatedTaint, Value: "true", Effect: corev1.TaintEffectNoSchedule}}
	spare.Spec.ProviderID = "aws:///zone/i-spare"
	if err = f.kube.Tracker().Add(spare); err != nil {
		t.Fatal(err)
	}
	other := *f.first
	other.Binding.RunID, other.Binding.UnitID, other.Binding.NodeID = "other-run", "other", "other"
	return &other
}

func TestClaimMeetingLivePlanPlacesOnPodDeletionWithoutReconcilePass(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newHandoffFixture(t)
		var passesAtCreate atomic.Int32
		passesAtCreate.Store(-1)
		f.kube.PrependReactor("create", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
			passesAtCreate.Store(f.passes.Load())
			return false, nil, nil
		})
		defer f.run(t)()
		initial := f.passes.Load()
		f.control.endPlan()
		f.control.deliveries <- f.first
		synctest.Wait()
		if f.job(t, f.first) || f.l.held(capacityName(f.first.Binding)) == nil {
			t.Fatal("first job is not waiting for its plan's Pod")
		}
		claimed := time.Now()
		time.Sleep(200 * time.Millisecond)
		f.deletePlanPod(t)
		synctest.Wait()
		if !f.job(t, f.first) {
			t.Fatal("first job still waiting after its plan's Pod was deleted")
		}
		if got := passesAtCreate.Load(); got != initial {
			t.Fatalf("Job created after %d reconcile passes, want none after the claim", got-initial)
		}
		if elapsed := time.Since(claimed); elapsed >= handoffWait {
			t.Fatalf("handoff took %s, want the Pod deletion to place it", elapsed)
		}
	})
}

func TestClaimRetiresEndedPlanWhilePreparing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newHandoffFixture(t)
		retired := make(chan struct{})
		var once sync.Once
		f.kube.PrependReactor("delete", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
			once.Do(func() { close(retired) })
			return false, nil, nil
		})
		overlapped := false
		f.control.prepare = func(ctx context.Context, _ capability.LeaseRef) {
			select {
			case <-retired:
				overlapped = true
			case <-time.After(time.Second):
			case <-ctx.Done():
			}
		}
		f.control.endPlan()
		if _, err := f.l.launch(t.Context(), f.first); !errors.Is(err, ErrNoDedicatedNode) {
			t.Fatalf("first job did not wait for the live planning Pod: %v", err)
		}
		if !overlapped {
			t.Fatal("plan retirement waited for Prepare to finish")
		}
		if len(f.first.handoffJobs) != 1 || f.first.handoffJobs[0] != f.plan.Labels[JobLabel] {
			t.Fatalf("handoff waits on %v, want the plan's Job", f.first.handoffJobs)
		}
	})
}

func TestHandoffWaitLeavesOtherAwardsUnblocked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newHandoffFixture(t)
		other := f.otherRun(t)
		defer f.run(t)()
		f.control.endPlan()
		f.control.deliveries <- f.first
		synctest.Wait()
		if f.first.handoffUntil.IsZero() || f.job(t, f.first) {
			t.Fatal("first job is not waiting for its plan's Pod")
		}
		f.control.deliveries <- other
		synctest.Wait()
		if !time.Now().Before(f.first.handoffUntil) {
			t.Fatal("another claim waited for the first job's handoff to end")
		}
		if !f.job(t, other) {
			t.Fatal("another award was not placed while the first job waited")
		}
	})
}

func TestHandoffWaitEndsAtItsBoundAndFallsBackToReconciliation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newHandoffFixture(t)
		defer f.run(t)()
		initial := f.passes.Load()
		f.control.endPlan()
		f.control.deliveries <- f.first
		synctest.Wait()
		until := f.first.handoffUntil
		if until.IsZero() {
			t.Fatal("first job is not waiting for its plan's Pod")
		}
		terminating := f.plan.DeepCopy()
		terminating.Status.Phase = corev1.PodSucceeded
		if _, err := f.kube.CoreV1().Pods("cloud").UpdateStatus(t.Context(), terminating, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if got := f.passes.Load(); got != initial {
			t.Fatalf("the awaited Pod's own update started %d reconcile passes during the wait", got-initial)
		}
		time.Sleep(time.Until(until))
		synctest.Wait()
		if got := f.passes.Load(); got != initial+1 {
			t.Fatalf("reconcile passes after the wait=%d, want the deferred one", got-initial)
		}
		if f.job(t, f.first) {
			t.Fatal("first job placed while the plan's Pod remained on the node")
		}
		time.Sleep(100 * time.Millisecond)
		f.deletePlanPod(t)
		synctest.Wait()
		if got := f.passes.Load(); got != initial+2 {
			t.Fatalf("reconcile passes after Pod deletion=%d, want the ordinary event pass", got-initial)
		}
		if !f.job(t, f.first) {
			t.Fatal("reconciliation did not place the first job after the wait ended")
		}
	})
}

func TestCancelledAwardLeavesHandoffWithoutAJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newHandoffFixture(t)
		defer f.run(t)()
		f.control.endPlan()
		f.control.deliveries <- f.first
		synctest.Wait()
		until := f.first.handoffUntil
		if until.IsZero() {
			t.Fatal("first job is not waiting for its plan's Pod")
		}
		f.control.refuse(f.first)
		time.Sleep(100 * time.Millisecond)
		f.deletePlanPod(t)
		synctest.Wait()
		if !time.Now().Before(until) {
			t.Fatal("cancellation was noticed only after the wait ended")
		}
		if f.l.held(capacityName(f.first.Binding)) != nil {
			t.Fatal("cancelled award still waiting")
		}
		if f.job(t, f.first) {
			t.Fatal("cancelled award created a Job")
		}
	})
}

func TestHandoffWaitKeepsRenewingTheAward(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newHandoffFixture(t)
		f.l.Poll = 300 * time.Millisecond
		defer f.run(t)()
		f.control.endPlan()
		f.control.deliveries <- f.first
		synctest.Wait()
		until := f.first.handoffUntil
		if until.IsZero() {
			t.Fatal("first job is not waiting for its plan's Pod")
		}
		claimed := f.control.preparations(f.first)
		time.Sleep(time.Until(until) - time.Millisecond)
		synctest.Wait()
		if got := f.control.preparations(f.first); got <= claimed {
			t.Fatalf("award preparation renewed %d times during the wait, want reconciliation to keep renewing it", got-claimed)
		}
	})
}

func TestClaimAfterPlanIsGoneTakesOneAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newHandoffFixture(t)
		f.control.endPlan()
		if err := f.l.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}
		f.deletePlanPod(t)
		if err := f.l.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.kube.BatchV1().Jobs("cloud").Get(t.Context(), f.plan.Labels[JobLabel], metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("plan Job remains: %v", err)
		}
		var syncs, reservations atomic.Int32
		f.l.Control = &countingSync{CloudControl: f.control, syncs: &syncs}
		f.kube.PrependReactor("list", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
			if !action.(ktesting.ListAction).GetListRestrictions().Labels.Empty() {
				reservations.Add(1)
			}
			return false, nil, nil
		})
		if _, err := f.l.launch(t.Context(), f.first); err != nil {
			t.Fatal(err)
		}
		if !f.job(t, f.first) || !f.first.handoffUntil.IsZero() {
			t.Fatal("first job did not place on its first attempt")
		}
		if got := syncs.Load(); got != 1 {
			t.Fatalf("controller decisions=%d, want only the activation check", got)
		}
		if got := reservations.Load(); got != 1 {
			t.Fatalf("reservation attempts=%d, want one", got)
		}
	})
}

// hack: a claim's team-scoped retirement also lists Jobs, so only an unscoped list marks a reconcile pass.
func reconcileList(action ktesting.Action) bool {
	_, scoped := action.(ktesting.ListAction).GetListRestrictions().Labels.RequiresExactMatch(TeamLabel)
	return !scoped
}

type countingSync struct {
	CloudControl
	syncs *atomic.Int32
}

func (c *countingSync) Sync(ctx context.Context, observations []CloudJobObservation) ([]CloudJobState, error) {
	c.syncs.Add(1)
	return c.CloudControl.Sync(ctx, observations)
}

func TestRefusedPreparationDoesNotWaitForStalledRetirement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newHandoffFixture(t)
		f.l.Poll = 2 * time.Second
		other := f.otherRun(t)
		defer f.run(t)()
		initial := f.passes.Load()
		f.control.endPlan()
		f.control.refuse(f.first)
		f.control.stallSync.Store(true)
		f.control.deliveries <- f.first
		select {
		case f.control.deliveries <- other:
		case <-time.After(time.Minute):
			t.Fatal("a refused award's stalled retirement stopped further claims")
		}
		synctest.Wait()
		if !f.job(t, other) {
			t.Fatal("another award was not placed after a refused award")
		}
		time.Sleep(f.l.Poll)
		synctest.Wait()
		if f.passes.Load() == initial {
			t.Fatal("reconciliation stopped behind a refused award's stalled retirement")
		}
		if f.job(t, f.first) {
			t.Fatal("refused award created a Job")
		}
	})
}

func TestQueuedPredecessorDeletionHandsOffBeforeTheFullPass(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newHandoffFixture(t)
		unrelated := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "cloud", Labels: map[string]string{"app.kubernetes.io/managed-by": managedByLauncher, JobLabel: "unrelated"}}}
		if _, err := f.kube.CoreV1().Pods("cloud").Create(t.Context(), unrelated, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		other := f.otherRun(t)
		gate := make(chan struct{})
		f.control.prepare = func(ctx context.Context, ref capability.LeaseRef) {
			if ref == other.Binding.LeaseRef() {
				select {
				case <-gate:
				case <-ctx.Done():
				}
			}
		}
		var passesAtCreate atomic.Int32
		passesAtCreate.Store(-1)
		f.kube.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
			if action.(ktesting.CreateAction).GetObject().(*batchv1.Job).Name == jobName(f.first.Binding) {
				passesAtCreate.Store(f.passes.Load())
			}
			return false, nil, nil
		})
		defer f.run(t)()
		f.control.endPlan()
		f.control.deliveries <- f.first
		synctest.Wait()
		if f.job(t, f.first) || f.first.handoffUntil.IsZero() {
			t.Fatal("first job is not waiting for its plan's Pod")
		}
		f.control.deliveries <- other
		synctest.Wait()
		// safety: the loop is held in the other award's Prepare, so both events queue in order.
		unrelated.Status.Phase = corev1.PodRunning
		if _, err := f.kube.CoreV1().Pods("cloud").UpdateStatus(t.Context(), unrelated, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		f.deletePlanPod(t)
		close(gate)
		synctest.Wait()
		if !f.job(t, f.first) {
			t.Fatal("first job still waiting after its plan's Pod was deleted")
		}
		if got, passes := passesAtCreate.Load(), f.passes.Load(); got >= passes {
			t.Fatalf("Job created after the full pass (passes at create=%d, after=%d)", got, passes)
		}
	})
}

func TestAwaitedPodUpdateAfterATimerPassStartsNoExtraPass(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newHandoffFixture(t)
		f.l.Poll = 300 * time.Millisecond
		defer f.run(t)()
		f.control.endPlan()
		f.control.deliveries <- f.first
		synctest.Wait()
		if f.job(t, f.first) || f.first.handoffUntil.IsZero() {
			t.Fatal("first job is not waiting for its plan's Pod")
		}
		time.Sleep(f.l.Poll + 50*time.Millisecond)
		synctest.Wait()
		before := f.passes.Load()
		updated := f.plan.DeepCopy()
		updated.Status.Phase = corev1.PodSucceeded
		if _, err := f.kube.CoreV1().Pods("cloud").UpdateStatus(t.Context(), updated, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if got := f.passes.Load(); got != before {
			t.Fatalf("the awaited Pod's update after a timer pass started %d extra passes", got-before)
		}
	})
}

func TestPreparationStageExcludesClaimTimeRetirement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newHandoffFixture(t)
		var logs bytes.Buffer
		f.l.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
		f.kube.PrependReactor("list", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
			if !reconcileList(action) {
				time.Sleep(500 * time.Millisecond)
			}
			return false, nil, nil
		})
		f.control.endPlan()
		if _, err := f.l.launch(t.Context(), f.first); !errors.Is(err, ErrNoDedicatedNode) {
			t.Fatalf("first job did not wait for the live planning Pod: %v", err)
		}
		stages := startupRecords(t, &logs)["launcher_preparation"]
		if len(stages) != 1 || stages[0]["duration_ms"] != float64(0) {
			t.Fatalf("preparation stage=%v, want Prepare's own instant duration", stages)
		}
	})
}
