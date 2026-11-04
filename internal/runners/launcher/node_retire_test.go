package launcher

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestRestartRetiresOnlyEmptySpentNodesAfterIdleGrace(t *testing.T) {
	for _, scenario := range []string{"idle", "daemon pod", "work pod", "terminal pod", "warm pod", "successor binds after cordon", "foreign team", "foreign run", "unlabeled", "missing annotation", "foreign pool", "claim UID changed", "node replaced", "concurrent reservation", "cordon conflict"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				boundary, kube := boundaryFixture()
				binding := boundaryBinding("team")
				held, err := boundary.Reserve(t.Context(), binding, jobName(binding), CloudPlacement{RequestedClass: "2"})
				if err != nil {
					t.Fatal(err)
				}
				if err = boundary.Release(t.Context(), held); err != nil {
					t.Fatal(err)
				}
				l, _, _, _ := fixture()
				l.Kube, l.Boundary = kube, boundary
				if err = l.Sync(t.Context()); err != nil {
					t.Fatal(err)
				}
				time.Sleep(DefaultIdleGrace)
				node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "foreign team":
					node.Labels[TeamNodeLabel] = TeamNodeValue("cloud", "foreign")
				case "foreign run":
					node.Labels[usedRunLabel] = runNodeValue("cloud", "foreign")
				case "unlabeled":
					delete(node.Labels, usedRunLabel)
				case "missing annotation":
					delete(node.Annotations, usedRunAnnotation)
				case "foreign pool":
					node.Labels[PoolNamespaceLabel] = "foreign"
				case "claim UID changed":
					node.OwnerReferences[0].UID = "replacement"
				}
				if _, err = kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
				if scenario == "node replaced" || scenario == "concurrent reservation" {
					kube.PrependReactor("get", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
						current, err := kube.Tracker().Get(corev1.SchemeGroupVersion.WithResource("nodes"), "", "node")
						if err != nil {
							return true, nil, err
						}
						changed := current.(*corev1.Node).DeepCopy()
						if scenario == "node replaced" {
							changed.UID = "replacement"
						} else {
							changed.ResourceVersion = "999"
							changed.Annotations[reservationAnnotation] = "new reservation"
						}
						return true, changed, nil
					})
				}
				if scenario == "cordon conflict" {
					kube.PrependReactor("update", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
						candidate := action.(ktesting.UpdateAction).GetObject().(*corev1.Node)
						if !candidate.Spec.Unschedulable {
							t.Fatal("expected cordon update")
						}
						current, lookupErr := kube.Tracker().Get(corev1.SchemeGroupVersion.WithResource("nodes"), "", "node")
						if lookupErr != nil {
							t.Fatal(lookupErr)
						}
						reserved := current.(*corev1.Node).DeepCopy()
						reserved.ResourceVersion = "999"
						reserved.Annotations[reservationAnnotation] = "new reservation"
						if updateErr := kube.Tracker().Update(corev1.SchemeGroupVersion.WithResource("nodes"), reserved, ""); updateErr != nil {
							t.Fatal(updateErr)
						}
						return false, nil, nil
					})
				}
				if scenario == "successor binds after cordon" {
					kube.PrependReactor("update", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
						if action.(ktesting.UpdateAction).GetObject().(*corev1.Node).Spec.Unschedulable {
							successor := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "capacity", Namespace: "cloud"}, Spec: corev1.PodSpec{NodeName: "node"}}
							if createErr := kube.Tracker().Add(successor); createErr != nil && !apierrors.IsAlreadyExists(createErr) {
								t.Fatal(createErr)
							}
						}
						return false, nil, nil
					})
				}
				if scenario == "daemon pod" || scenario == "work pod" || scenario == "terminal pod" || scenario == "warm pod" {
					pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "occupant", Namespace: "foreign"}, Spec: corev1.PodSpec{NodeName: "node"}}
					if scenario == "daemon pod" {
						pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "daemon", UID: "daemon-uid", Controller: ptr(true)}}
					}
					if scenario == "terminal pod" {
						pod.Status.Phase = corev1.PodSucceeded
					}
					if scenario == "warm pod" {
						pod = sharedWarmPod()
					}
					if _, err = kube.CoreV1().Pods(pod.Namespace).Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
						t.Fatal(err)
					}
				}
				karpenter := boundary.Karpenter.(*dynamicfake.FakeDynamicClient)
				karpenter.PrependReactor("delete", "nodeclaims", func(action ktesting.Action) (bool, runtime.Object, error) {
					options := action.(ktesting.DeleteAction).GetDeleteOptions()
					if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != "claim-uid" {
						t.Fatalf("unfenced deletion: %+v", options)
					}
					return false, nil, nil
				})
				err = l.Sync(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				_, err = boundary.Karpenter.Resource(nodeClaims).Get(t.Context(), "claim", metav1.GetOptions{})
				retired := scenario == "idle" || scenario == "daemon pod"
				if apierrors.IsNotFound(err) != retired {
					t.Fatalf("retired=%v want=%v: %v", apierrors.IsNotFound(err), retired, err)
				}
				current, lookupErr := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
				if lookupErr != nil {
					t.Fatal(lookupErr)
				}
				if !retired && current.Spec.Unschedulable {
					t.Fatal("retirement left a kept node cordoned, so no successor could reserve it")
				}
				if scenario == "cordon conflict" && (current.Spec.Unschedulable || current.Annotations[reservationAnnotation] != "new reservation") {
					t.Fatalf("retirement overwrote concurrent reservation: %+v", current)
				}
				if scenario == "cordon conflict" {
					return
				}
				if err = l.Sync(t.Context()); err != nil {
					t.Fatalf("repeated reconciliation: %v", err)
				}
			})
		})
	}
}

func TestSpentNodeErrorsAreRateLimitedAndDoNotFailSync(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		boundary, kube := boundaryFixture()
		kube.PrependReactor("list", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
			if action.(ktesting.ListAction).GetListRestrictions().Labels.String() == "" {
				return false, nil, nil
			}
			return true, nil, errors.New("retirement unavailable")
		})
		l, _, _, control := fixture()
		var logs bytes.Buffer
		l.Kube, l.Boundary, l.Control = kube, boundary, control
		l.Logger = slog.New(slog.NewTextHandler(&logs, nil))
		for range 3 {
			if err := l.Sync(t.Context()); err != nil {
				t.Fatalf("retirement blocked sync: %v", err)
			}
		}
		if got := bytes.Count(logs.Bytes(), []byte("retirement unavailable")); got != 1 {
			t.Fatalf("logged error %d times", got)
		}
		time.Sleep(time.Minute)
		if err := l.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := bytes.Count(logs.Bytes(), []byte("retirement unavailable")); got != 2 {
			t.Fatalf("persistent error was not logged again: %d", got)
		}
	})
}

func addSpentPoolNode(t *testing.T, boundary *KubernetesBoundary, kube *fake.Clientset, name string) {
	t.Helper()
	node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node = node.DeepCopy()
	node.Name, node.UID, node.ResourceVersion, node.Spec.ProviderID = name, types.UID(name+"-uid"), "1", "aws:///zone/i-"+name
	node.OwnerReferences[0].Name, node.OwnerReferences[0].UID = name+"-claim", types.UID(name+"-claim-uid")
	if _, err = kube.CoreV1().Nodes().Create(t.Context(), node, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	claims := boundary.Karpenter.Resource(nodeClaims)
	claim, err := claims.Get(t.Context(), "claim", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	claim = claim.DeepCopy()
	claim.SetName(name + "-claim")
	claim.SetUID(types.UID(name + "-claim-uid"))
	claim.SetResourceVersion("")
	if err = unstructured.SetNestedField(claim.Object, name, "status", "nodeName"); err != nil {
		t.Fatal(err)
	}
	if err = unstructured.SetNestedField(claim.Object, node.Spec.ProviderID, "status", "providerID"); err != nil {
		t.Fatal(err)
	}
	if _, err = claims.Create(t.Context(), claim, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func idleScenario(t *testing.T, body func(t *testing.T, boundary *KubernetesBoundary, kube *fake.Clientset, at func(time.Duration), live func(claim string) bool)) {
	synctest.Test(t, func(t *testing.T) {
		boundary, kube := boundaryFixture()
		l, _, _, _ := fixture()
		l.Kube, l.Boundary = kube, boundary
		start := time.Now()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { l.retireLoop(ctx); close(done) }()
		body(t, boundary, kube, func(offset time.Duration) {
			time.Sleep(time.Until(start.Add(offset)))
			synctest.Wait()
		}, func(claim string) bool {
			_, err := boundary.Karpenter.Resource(nodeClaims).Get(t.Context(), claim, metav1.GetOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				t.Fatal(err)
			}
			return err == nil
		})
		cancel()
		<-done
	})
}

func reserveFor(t *testing.T, boundary *KubernetesBoundary, job string) CloudNodeReservation {
	t.Helper()
	binding := boundaryBinding("team")
	binding.UnitID = job
	held, err := boundary.Reserve(t.Context(), binding, job, CloudPlacement{RequestedClass: "2"})
	if err != nil {
		t.Fatalf("reserve %s: %v", job, err)
	}
	return held
}

func TestIdleGraceRetiresSpentNodesWhateverTheRunState(t *testing.T) {
	t.Run("approval pause longer than the grace retires the node", func(t *testing.T) {
		idleScenario(t, func(t *testing.T, boundary *KubernetesBoundary, _ *fake.Clientset, at func(time.Duration), live func(string) bool) {
			if err := boundary.Release(t.Context(), reserveFor(t, boundary, "build")); err != nil {
				t.Fatal(err)
			}
			at(DefaultIdleGrace - time.Second)
			if !live("claim") {
				t.Fatal("node retired inside the idle grace")
			}
			at(DefaultIdleGrace + retirementInterval)
			if live("claim") {
				t.Fatal("node idle past the grace was kept while its run waited on an approval")
			}
		})
	})
	t.Run("successor within the grace reuses the node", func(t *testing.T) {
		idleScenario(t, func(t *testing.T, boundary *KubernetesBoundary, _ *fake.Clientset, at func(time.Duration), live func(string) bool) {
			if err := boundary.Release(t.Context(), reserveFor(t, boundary, "build")); err != nil {
				t.Fatal(err)
			}
			at(45 * time.Second)
			successor := reserveFor(t, boundary, "deploy")
			if successor.NodeName != "node" {
				t.Fatalf("successor placed on %q, want the spent node", successor.NodeName)
			}
			at(5 * time.Minute)
			if !live("claim") {
				t.Fatal("reserved node was retired")
			}
			if err := boundary.Release(t.Context(), successor); err != nil {
				t.Fatal(err)
			}
			at(5*time.Minute + DefaultIdleGrace - time.Second)
			if !live("claim") {
				t.Fatal("reservation did not restart the idle clock")
			}
			at(5*time.Minute + DefaultIdleGrace + retirementInterval)
			if live("claim") {
				t.Fatal("node idle past the grace after its successor was kept")
			}
		})
	})
	t.Run("fan-out with one long sibling retires the finished siblings' nodes", func(t *testing.T) {
		idleScenario(t, func(t *testing.T, boundary *KubernetesBoundary, kube *fake.Clientset, at func(time.Duration), live func(string) bool) {
			addSpentPoolNode(t, boundary, kube, "second")
			short, long := reserveFor(t, boundary, "short"), reserveFor(t, boundary, "long")
			if short.NodeName == long.NodeName {
				t.Fatal("fan-out shared one node")
			}
			if err := boundary.Release(t.Context(), short); err != nil {
				t.Fatal(err)
			}
			claim := map[string]string{"node": "claim", "second": "second-claim"}
			at(DefaultIdleGrace + retirementInterval)
			if live(claim[short.NodeName]) {
				t.Fatal("finished sibling's node was held for the long sibling")
			}
			if !live(claim[long.NodeName]) {
				t.Fatal("long sibling's reserved node was retired")
			}
		})
	})
}

func TestIdleGraceMustOutlastClaimPollAndRetirementPass(t *testing.T) {
	l, _, _, _ := fixture()
	l.Poll, l.IdleGrace = 5*time.Second, 34*time.Second
	if err := l.validate(); err == nil {
		t.Fatal("grace shorter than claim poll plus a retirement pass was accepted")
	}
	l.IdleGrace = 0
	if err := l.validate(); err != nil {
		t.Fatalf("default grace: %v", err)
	}
}
