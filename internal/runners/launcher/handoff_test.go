package launcher

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestSuccessorWaitsForPredecessorNode(t *testing.T) {
	l, kube, _, control := fixture()
	boundary, nodes := boundaryFixture()
	node, err := nodes.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = kube.Tracker().Add(node); err != nil {
		t.Fatal(err)
	}
	boundary.Kube = kube
	l.Boundary = boundary
	l.Config.MemoryCeiling = 16 << 30
	control.award.CPUCores = 2
	control.award.Memory = match.CloudSizeMemoryBytes(2)
	predecessor := *control.award
	if _, err = l.launch(t.Context(), &predecessor); err != nil {
		t.Fatal(err)
	}
	job, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(predecessor.Binding), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: *job.Spec.Template.ObjectMeta.DeepCopy(), Spec: *job.Spec.Template.Spec.DeepCopy()}
	pod.Name, pod.Namespace, pod.UID = "predecessor", "cloud", "predecessor-pod"
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr(true)}}
	pod.Status.Phase = corev1.PodRunning
	if _, err = kube.CoreV1().Pods("cloud").Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	control.remove = true
	if err = l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	// safety: a ready fresh node must not win the handoff while the old one is deleting.
	fresh := node.DeepCopy()
	fresh.Name, fresh.UID = "a-fresh", "fresh-uid"
	fresh.OwnerReferences[0].Name, fresh.OwnerReferences[0].UID = "fresh-claim", "fresh-claim-uid"
	claim, err := boundary.Karpenter.Resource(nodeClaims).Get(t.Context(), "claim", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	claim.SetName("fresh-claim")
	claim.SetUID("fresh-claim-uid")
	if err = unstructured.SetNestedField(claim.Object, fresh.Name, "status", "nodeName"); err != nil {
		t.Fatal(err)
	}
	if _, err = boundary.Karpenter.Resource(nodeClaims).Create(t.Context(), claim, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = kube.CoreV1().Nodes().Create(t.Context(), fresh, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	successor := predecessor
	successor.Binding.UnitID, successor.Binding.NodeID = "successor", "successor"
	control.award, control.remove = &successor, false
	if _, err = l.launch(t.Context(), &successor); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatalf("pending teardown: %v", err)
	}
	requests, err := l.capacityPods(t.Context())
	if err != nil || len(requests) != 0 {
		t.Fatalf("successor provisioned during teardown: %d requests, %v", len(requests), err)
	}
	if err = kube.CoreV1().Pods("cloud").Delete(t.Context(), pod.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = l.placeWaiting(t.Context()); err != nil {
		t.Fatal(err)
	}
	next, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(successor.Binding), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, reservation, err := jobOwnership(*next)
	if err != nil || reservation.NodeUID != string(node.UID) {
		t.Fatalf("successor lost original node: %+v, %v", reservation, err)
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "create" && action.GetResource().Resource == "pods" && action.(ktesting.CreateAction).GetObject().(*corev1.Pod).Labels[capacityLabel] == "true" {
			t.Fatal("handoff created a provisioning Pod")
		}
	}
	requests, err = l.capacityPods(t.Context())
	if err != nil || len(requests) != 0 {
		t.Fatalf("unexpected capacity request: %d, %v", len(requests), err)
	}
}

func TestTeardownWaitIsBoundedAndRunScoped(t *testing.T) {
	for _, scenario := range []string{"same run", "planning predecessor", "other run", "other team", "live predecessor", "foreign pod", "wrong class", "quarantined", "wrong binding", "expired wait"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l, kube, _, control := fixture()
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
				predecessor := *control.award
				predecessor.CPUCores, predecessor.Memory = 2, match.CloudSizeMemoryBytes(2)
				if scenario == "planning predecessor" {
					predecessor.Binding.Kind, predecessor.Binding.NodeID = CloudPlan, ""
					predecessor.Binding.Attempt = 0
				}
				if _, err = l.launch(t.Context(), &predecessor); err != nil {
					t.Fatal(err)
				}
				job, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(predecessor.Binding), metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if scenario != "live predecessor" && scenario != "planning predecessor" {
					foreground := metav1.DeletePropagationForeground
					if err = kube.BatchV1().Jobs("cloud").Delete(t.Context(), job.Name, metav1.DeleteOptions{PropagationPolicy: &foreground, Preconditions: &metav1.Preconditions{UID: &job.UID}}); err != nil {
						t.Fatal(err)
					}
				}
				successor := predecessor
				successor.Binding.Kind = CloudWork
				successor.Binding.UnitID, successor.Binding.NodeID = "next", "next"
				switch scenario {
				case "other run":
					successor.Binding.RunID = "other"
				case "other team":
					successor.Binding.Team = "other"
				case "expired wait":
					successor.teardownDeadline = time.Now().Add(-time.Second)
				case "wrong class":
					successor.CPUCores, successor.Memory = 4, match.CloudSizeMemoryBytes(4)
					l.Config.MemoryCeiling = 32 << 30
				case "foreign pod":
					_, err = kube.CoreV1().Pods("foreign").Create(t.Context(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "foreign", Namespace: "foreign"}, Spec: corev1.PodSpec{NodeName: "node"}}, metav1.CreateOptions{})
				case "quarantined", "wrong binding":
					node, err = kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
					if err != nil {
						t.Fatal(err)
					}
					if scenario == "quarantined" {
						node.Annotations[quarantineAnnotation] = "fault"
					} else {
						node.Annotations[bindingAnnotation] = "{}"
					}
					_, err = kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{})
				}
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "wrong class" {
					_, err = BuildJob(t.Context(), time.Now(), l.Config, successor, boundary)
					if !errors.Is(err, ErrNoDedicatedNode) || errors.Is(err, errPredecessorTeardown) {
						t.Fatalf("wrong-class teardown deferred: %v", err)
					}
					return
				}
				if _, err = l.launch(t.Context(), &successor); !errors.Is(err, ErrNoDedicatedNode) {
					t.Fatalf("launch: %v", err)
				}
				requests, err := l.capacityPods(t.Context())
				want := 1
				if scenario == "same run" || scenario == "planning predecessor" {
					want = 0
				}
				if err != nil || len(requests) != want {
					t.Fatalf("requests=%d want=%d: %v", len(requests), want, err)
				}
				if scenario == "same run" || scenario == "planning predecessor" {
					deadline := successor.teardownDeadline
					if !deadline.Equal(time.Now().Add(30 * time.Second)) {
						t.Fatalf("unexpected initial deadline: %v", deadline)
					}
					for range 2 {
						time.Sleep(10 * time.Second)
						if err = l.placeWaiting(t.Context()); err != nil {
							t.Fatal(err)
						}
						if !successor.teardownDeadline.Equal(deadline) {
							t.Fatalf("retry moved teardown deadline: %v", successor.teardownDeadline)
						}
						requests, err = l.capacityPods(t.Context())
						if err != nil || len(requests) != 0 {
							t.Fatalf("retry provisioned before deadline: %d, %v", len(requests), err)
						}
					}
					time.Sleep(10 * time.Second)
					if err = l.placeWaiting(t.Context()); err != nil {
						t.Fatal(err)
					}
					requests, err = l.capacityPods(t.Context())
					if err != nil || len(requests) != 1 {
						t.Fatalf("retry failed to provision at fixed deadline: %d, %v", len(requests), err)
					}
					if !successor.teardownDeadline.Equal(deadline) {
						t.Fatal("fallback moved teardown deadline")
					}
				}
			})
		})
	}
}

type leasedControl struct {
	*controlFake
	mu        sync.Mutex
	expires   map[capability.LeaseRef]time.Time
	lapsed    bool
	claimed   int
	cancelled bool
}

func (c *leasedControl) Prepare(ctx context.Context, fences []capability.LeaseRef) (time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancelled {
		return time.Time{}, &apierr.Error{Code: apierr.LockHeld, Message: "award cancelled"}
	}
	now := time.Now()
	for _, fence := range fences {
		if expires, ok := c.expires[fence]; ok && !now.Before(expires) {
			c.lapsed = true
			return time.Time{}, &apierr.Error{Code: apierr.LockHeld, Message: "lease lapsed"}
		}
		c.expires[fence] = now.Add(time.Minute)
	}
	return now.Add(time.Minute), context.Cause(ctx)
}

func (c *leasedControl) Claim(context.Context, string, []string) (*CloudJobAward, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.claimed++
	return nil, &apierr.Error{Code: apierr.RateLimited, Message: "poll later", Details: map[string]any{"retry_after_ms": float64(10 * time.Minute / time.Millisecond)}}
}

func TestTeardownWaitRenewsThroughClaimBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, kube, _, control := fixture()
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
		leased := &leasedControl{controlFake: control, expires: map[capability.LeaseRef]time.Time{}}
		l.Control, l.Poll = leased, 5*time.Second
		predecessor := *control.award
		predecessor.CPUCores, predecessor.Memory = 2, match.CloudSizeMemoryBytes(2)
		if _, err = l.launch(t.Context(), &predecessor); err != nil {
			t.Fatal(err)
		}
		job, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(predecessor.Binding), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		pod := &corev1.Pod{ObjectMeta: *job.Spec.Template.ObjectMeta.DeepCopy(), Spec: *job.Spec.Template.Spec.DeepCopy()}
		pod.Name, pod.Namespace, pod.UID = "predecessor", "cloud", "predecessor-pod"
		pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr(true)}}
		pod.Status.Phase = corev1.PodRunning
		if _, err = kube.CoreV1().Pods("cloud").Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		control.remove = true
		if err = l.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}
		control.remove = false
		successor := predecessor
		successor.Binding.UnitID, successor.Binding.NodeID = "successor", "successor"
		control.award = &successor
		if _, err = l.launch(t.Context(), &successor); !errors.Is(err, ErrNoDedicatedNode) {
			t.Fatalf("pending teardown: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error)
		go func() { done <- l.Run(ctx) }()
		time.Sleep(15 * time.Second)
		if err = kube.CoreV1().Pods("cloud").Delete(t.Context(), pod.Name, metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Minute)
		cancel()
		if err = <-done; err != nil {
			t.Fatal(err)
		}
		leased.mu.Lock()
		defer leased.mu.Unlock()
		if leased.lapsed || leased.claimed != 1 {
			t.Fatalf("lapsed=%v claims=%d, want renewal through one long claim backoff", leased.lapsed, leased.claimed)
		}
		next, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(successor.Binding), metav1.GetOptions{})
		if err != nil {
			t.Fatalf("successor was not placed during claim backoff: %v", err)
		}
		if _, reservation, err := jobOwnership(*next); err != nil || reservation.NodeUID != string(node.UID) {
			t.Fatalf("successor lost original node: %+v, %v", reservation, err)
		}
	})
}

func TestCancelledWaitingAwardPreservesClaimBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const poll = 5 * time.Second
		const backoff = 10 * time.Minute
		l, _, boundary, control := fixture()
		leased := &leasedControl{controlFake: control, expires: map[capability.LeaseRef]time.Time{}}
		l.Control, l.Poll = leased, poll
		boundary.reserveErr = errPredecessorTeardown
		if _, err := l.launch(t.Context(), control.award); !errors.Is(err, ErrNoDedicatedNode) {
			t.Fatalf("pending teardown: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error)
		go func() { done <- l.Run(ctx) }()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}()
		synctest.Wait()
		leased.mu.Lock()
		claims := leased.claimed
		leased.mu.Unlock()
		if claims != 1 {
			t.Fatalf("initial claims=%d, want one controller backoff", claims)
		}
		time.Sleep(poll)
		synctest.Wait()
		leased.mu.Lock()
		leased.cancelled = true
		leased.mu.Unlock()
		time.Sleep(poll)
		synctest.Wait()
		if l.held(capacityName(control.award.Binding)) != nil {
			t.Fatal("cancelled award is still waiting for placement")
		}
		time.Sleep(backoff - 2*poll - time.Nanosecond)
		synctest.Wait()
		leased.mu.Lock()
		claims = leased.claimed
		leased.mu.Unlock()
		if claims != 1 {
			t.Fatalf("claims before backoff deadline=%d, want 1", claims)
		}
		time.Sleep(poll)
		synctest.Wait()
		leased.mu.Lock()
		claims = leased.claimed
		leased.mu.Unlock()
		if claims != 2 {
			t.Fatalf("claims after backoff deadline=%d, want 2", claims)
		}
	})
}
