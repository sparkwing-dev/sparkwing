package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func reservedNode(t *testing.T, name string, binding CloudJobBinding, reservation string) *corev1.Node {
	t.Helper()
	if reservation == "" {
		raw, err := json.Marshal(CloudNodeReservation{NodeName: name, NodeUID: name + "-uid", Team: binding.Team, JobName: jobName(binding), ReservationID: "r-" + name, Award: binding.LeaseRef()})
		if err != nil {
			t.Fatal(err)
		}
		reservation = string(raw)
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name + "-uid"), Labels: map[string]string{TeamNodeLabel: TeamNodeValue("cloud", binding.Team)}, Annotations: map[string]string{reservationNamespaceAnnotation: "cloud", reservationAnnotation: reservation, bindingAnnotation: string(raw)}}}
}

// A node can rewrite its own annotations, so its bad reservation must cost only that node.
func TestRunQuarantinesTamperedNodeAndKeepsClaiming(t *testing.T) {
	for name, test := range map[string]struct {
		reservation       string
		refuse, apiOutage bool
	}{
		"malformed":        {reservation: "garbage"},
		"refused by award": {refuse: true},
		"api outage":       {reservation: "garbage", apiOutage: true},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l, kube, boundary, control := fixture()
				tampered, healthy := boundaryBinding("hostile"), boundaryBinding("team")
				tampered.UnitID, healthy.UnitID = "tampered", "healthy"
				for _, node := range []*corev1.Node{reservedNode(t, "tampered", tampered, test.reservation), reservedNode(t, "healthy", healthy, "")} {
					if _, err := kube.CoreV1().Nodes().Create(t.Context(), node, metav1.CreateOptions{}); err != nil {
						t.Fatal(err)
					}
				}
				if test.apiOutage {
					kube.PrependReactor("list", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
						return true, nil, errors.New("API server unavailable")
					})
				}
				claimed := make(chan struct{})
				l.Poll = time.Second
				l.Control = claimSignalControl{CloudControl: publicationControl{CloudControl: control, sync: func(observed []CloudJobObservation) ([]CloudJobState, error) {
					if test.refuse && observed[0].NodeUID == "tampered-uid" {
						return nil, apierr.Authorization(apierr.ReasonBinding, "unknown award fence")
					}
					return []CloudJobState{{Binding: observed[0].Binding, LeaseExpiresAt: time.Now().Add(time.Hour), Delete: true}}, nil
				}}, claimed: claimed}
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan error, 1)
				go func() { done <- l.Run(ctx) }()
				synctest.Wait()
				reached := false
				select {
				case <-claimed:
					reached = true
				default:
				}
				cancel()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				node, err := kube.CoreV1().Nodes().Get(t.Context(), "tampered", metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if test.apiOutage {
					if reached {
						t.Fatal("claimed through a cluster-wide reconciliation failure")
					}
					return
				}
				if !reached {
					t.Fatal("one node's reservation stopped claims for every team")
				}
				if !node.Spec.Unschedulable || node.Annotations[quarantineAnnotation] == "" || !slices.ContainsFunc(node.Spec.Taints, func(t corev1.Taint) bool { return t.Key == quarantineAnnotation }) {
					t.Fatalf("tampered node was not quarantined: unschedulable=%v annotations=%v", node.Spec.Unschedulable, node.Annotations)
				}
				if boundary.releases != 1 {
					t.Fatalf("healthy orphaned reservation releases = %d", boundary.releases)
				}
			})
		})
	}
}

func TestReserveSkipsQuarantinedNode(t *testing.T) {
	b, kube := boundaryFixture()
	node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Annotations = map[string]string{quarantineAnnotation: "reservation is malformed or does not match its award"}
	if _, err = kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Reserve(t.Context(), boundaryBinding("team"), "job"); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatalf("an uncordoned quarantined node was reserved: %v", err)
	}
}

func TestBlockingKeepsOnlyClusterFaults(t *testing.T) {
	outage := errors.New("API server unavailable")
	fault := &nodeFault{node: "n", cause: "quarantined: x"}
	if err := blocking(errors.Join(fault, errors.Join(fault, nil))); err != nil {
		t.Fatalf("node faults blocked claims: %v", err)
	}
	if err := blocking(errors.Join(fault, outage)); !errors.Is(err, outage) {
		t.Fatalf("cluster fault hidden by a node fault: %v", err)
	}
	if err := blocking(errors.Join(errors.New("wrapped"), errAwardMismatch)); err == nil {
		t.Fatal("an unattributed mismatch was dropped")
	}
}

// A quarantine that has not persisted must not let claims continue, except
// under conflicts, which a node causes by rewriting itself and which leave
// its recorded reservation keeping placement away.
func TestQuarantineWriteFailures(t *testing.T) {
	for name, test := range map[string]struct {
		failures int
		failure  error
		claims   bool
		persist  bool
	}{
		"unavailable":         {failures: 1 << 20, failure: apierrors.NewServiceUnavailable("etcd leader change")},
		"one conflict":        {failures: 1, failure: apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, "tampered", errors.New("changed")), claims: true, persist: true},
		"persistent conflict": {failures: 1 << 20, failure: apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, "tampered", errors.New("changed")), claims: true},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l, kube, _, control := fixture()
				tampered := boundaryBinding("hostile")
				if _, err := kube.CoreV1().Nodes().Create(t.Context(), reservedNode(t, "tampered", tampered, "garbage"), metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
				failures := test.failures
				kube.PrependReactor("patch", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
					if failures == 0 {
						return false, nil, nil
					}
					failures--
					return true, nil, test.failure
				})
				claimed := make(chan struct{})
				l.Poll = time.Second
				l.Control = claimSignalControl{CloudControl: control, claimed: claimed}
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan error, 1)
				go func() { done <- l.Run(ctx) }()
				// safety: conflict retries back off in virtual time before the pass claims.
				time.Sleep(10 * time.Second)
				synctest.Wait()
				reached := false
				select {
				case <-claimed:
					reached = true
				default:
				}
				cancel()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if reached != test.claims {
					t.Fatalf("claimed=%v, want %v", reached, test.claims)
				}
				node, err := kube.CoreV1().Nodes().Get(t.Context(), "tampered", metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if persisted := quarantined(*node) && node.Spec.Unschedulable && slices.ContainsFunc(node.Spec.Taints, func(t corev1.Taint) bool { return t.Key == quarantineAnnotation }); persisted != test.persist {
					t.Fatalf("quarantine persisted=%v, want %v: %+v", persisted, test.persist, node.Spec)
				}
			})
		})
	}
}

func TestNodeFaultsQuarantineEveryAuthorizedNodeWithoutStoppingClaims(t *testing.T) {
	l, kube, _, control := fixture()
	for i := range 4 {
		binding := boundaryBinding("team")
		binding.UnitID = "unit-" + string(rune('a'+i))
		node := reservedNode(t, "node-"+string(rune('a'+i)), binding, "garbage")
		if i%2 == 0 {
			delete(node.Annotations, reservationNamespaceAnnotation)
		} else {
			node.Annotations[reservationNamespaceAnnotation] = "elsewhere"
		}
		if _, err := kube.CoreV1().Nodes().Create(t.Context(), node, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	other := boundaryBinding("other")
	if _, err := kube.CoreV1().Nodes().Create(t.Context(), reservedNode(t, "other-node", other, "garbage"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	foreign := reservedNode(t, "foreign-node", other, "garbage")
	foreign.Labels[TeamNodeLabel] = TeamNodeValue("elsewhere", other.Team)
	foreign.Annotations[reservationNamespaceAnnotation] = "cloud"
	if _, err := kube.CoreV1().Nodes().Create(t.Context(), foreign, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	err := l.Sync(t.Context())
	if err == nil || blocking(err) != nil || !strings.Contains(err.Error(), "node-d") {
		t.Fatalf("fault was not confined to its team: %v", err)
	}
	nodes, err := kube.CoreV1().Nodes().List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tainted := 0
	for _, node := range nodes.Items {
		if quarantined(node) {
			tainted++
		}
	}
	if tainted != 5 {
		t.Fatalf("quarantined %d nodes in one pass, want %d", tainted, 5)
	}
	control.award = nil
	if _, err := l.LaunchOne(t.Context()); err != nil || len(control.excluded) != 0 {
		t.Fatalf("node faults excluded healthy team claims: excluded=%v err=%v", control.excluded, err)
	}
	if err := l.Sync(t.Context()); blocking(err) != nil {
		t.Fatalf("the next pass crossed the team boundary: %v", err)
	}
	if err := kube.CoreV1().Nodes().Delete(t.Context(), "node-a", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := l.Sync(t.Context()); blocking(err) != nil {
		t.Fatalf("sync after operator cleanup: %v", err)
	}
	if _, err := l.LaunchOne(t.Context()); err != nil || len(control.excluded) != 0 {
		t.Fatalf("operator cleanup stopped healthy team claims: excluded=%v err=%v", control.excluded, err)
	}
}

func TestErasedReservationIdentityReportsNodesWithoutStoppingTeam(t *testing.T) {
	l, kube, _, control := fixture()
	for i := range 4 {
		binding := boundaryBinding("team")
		name := fmt.Sprintf("erased-%d", i)
		node := reservedNode(t, name, binding, "")
		node.Annotations = nil
		node.Spec.Taints = []corev1.Taint{{Key: reservationTaint, Value: "r-" + name, Effect: corev1.TaintEffectNoSchedule}}
		if _, err := kube.CoreV1().Nodes().Create(t.Context(), node, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	foreign := reservedNode(t, "foreign", boundaryBinding("team"), "")
	foreign.Labels[TeamNodeLabel] = TeamNodeValue("elsewhere", "team")
	foreign.Annotations = nil
	foreign.Spec.Taints = []corev1.Taint{{Key: reservationTaint, Effect: corev1.TaintEffectNoSchedule}}
	if _, err := kube.CoreV1().Nodes().Create(t.Context(), foreign, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		free := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("free-%d", i), Labels: map[string]string{TeamNodeLabel: TeamNodeValue("cloud", "free")}}}
		if _, err := kube.CoreV1().Nodes().Create(t.Context(), free, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Sync(t.Context()); err == nil || blocking(err) != nil || !strings.Contains(err.Error(), "erased-3") {
		t.Fatalf("erased node identity was not reported: %v", err)
	}
	control.award = nil
	if _, err := l.LaunchOne(t.Context()); err != nil || len(control.excluded) != 0 {
		t.Fatalf("erased reservations stopped healthy team claims: excluded=%v err=%v", control.excluded, err)
	}
	nodes, err := kube.CoreV1().Nodes().List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes.Items {
		if quarantined(node) {
			t.Fatalf("unattributed node %s was quarantined", node.Name)
		}
	}
}

func TestReleaseConflictsLeaveHealthyTeamClaimsAvailable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, kube, _, control := fixture()
		l.Boundary = &KubernetesBoundary{Kube: kube, Namespace: l.Config.Namespace}
		control.remove, control.award = true, nil
		for i := range 4 {
			binding := boundaryBinding("team")
			binding.UnitID = "unit-" + string(rune('a'+i))
			node := reservedNode(t, "node-"+string(rune('a'+i)), binding, "")
			if _, err := kube.CoreV1().Nodes().Create(t.Context(), node, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		other := reservedNode(t, "other", boundaryBinding("other"), "")
		if _, err := kube.CoreV1().Nodes().Create(t.Context(), other, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		kube.PrependReactor("update", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
			node := action.(ktesting.UpdateAction).GetObject().(*corev1.Node)
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, node.Name, errors.New("changed"))
		})
		if err := l.Sync(t.Context()); err == nil || blocking(err) != nil {
			t.Fatalf("release faults were not contained: %v", err)
		}
		if _, err := l.LaunchOne(t.Context()); err != nil || len(control.excluded) != 0 {
			t.Fatalf("release faults stopped healthy team claims: excluded=%v err=%v", control.excluded, err)
		}
		if err := kube.CoreV1().Nodes().Delete(t.Context(), "node-a", metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := l.Sync(t.Context()); blocking(err) != nil {
			t.Fatal(err)
		}
		if _, err := l.LaunchOne(t.Context()); err != nil || len(control.excluded) != 0 {
			t.Fatalf("repair stopped healthy team claims: %v %v", control.excluded, err)
		}
	})
}

func TestPriorTeardownFaultsDoNotLimitQuarantine(t *testing.T) {
	l, kube, _, control := fixture()
	var prior []error
	for i := range 7 {
		binding := boundaryBinding("team")
		binding.UnitID = fmt.Sprintf("unit-%d", i)
		node := reservedNode(t, fmt.Sprintf("node-%d", i), binding, "")
		delete(node.Annotations, bindingAnnotation)
		if _, err := kube.CoreV1().Nodes().Create(t.Context(), node, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		fault := &nodeFault{node: node.Name, cause: "reservation changed before teardown"}
		prior = append(prior, errors.Join(fault, fault))
	}
	if err := l.syncReservations(t.Context(), errors.Join(prior...)); blocking(err) != nil {
		t.Fatalf("contained faults stopped other teams: %v", err)
	}
	nodes, err := kube.CoreV1().Nodes().List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	quarantines := 0
	for _, node := range nodes.Items {
		if quarantined(node) {
			quarantines++
		}
	}
	if quarantines != 7 {
		t.Fatalf("prior faults limited quarantine to %d nodes, want %d", quarantines, 7)
	}
	control.award = nil
	if _, err := l.LaunchOne(t.Context()); err != nil || len(control.excluded) != 0 {
		t.Fatalf("node faults stopped healthy team claims: excluded=%v err=%v", control.excluded, err)
	}
}

func TestOperatorStopsOnlyNamedTeamClaims(t *testing.T) {
	l, _, _, control := fixture()
	l.Config.StoppedTeams = []string{"team", "another"}
	control.award = nil
	if _, err := l.LaunchOne(t.Context()); err != nil || !slices.Equal(control.excluded, []string{TeamNodeValue("cloud", "team"), TeamNodeValue("cloud", "another")}) {
		t.Fatalf("operator stop was not passed to claims: excluded=%v err=%v", control.excluded, err)
	}
	l.Config.StoppedTeams = nil
	if _, err := l.LaunchOne(t.Context()); err != nil || len(control.excluded) != 0 {
		t.Fatalf("operator resume retained a stop: excluded=%v err=%v", control.excluded, err)
	}
}

func TestOperatorStopHoldsWaitingPlacement(t *testing.T) {
	l, kube, _, control := fixture()
	l.Config.StoppedTeams = []string{"team"}
	l.wait(control.award)
	control.award = nil
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs, err := kube.BatchV1().Jobs(l.Config.Namespace).List(t.Context(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 0 || len(l.waitingAwards()) != 1 {
		t.Fatalf("operator stop allowed waiting placement: jobs=%v err=%v", jobs, err)
	}
	l.Config.StoppedTeams = nil
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs, err = kube.BatchV1().Jobs(l.Config.Namespace).List(t.Context(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 {
		t.Fatalf("operator resume did not place held award: jobs=%v err=%v", jobs, err)
	}
}

func TestReservationBindingMismatchStaysExcludedDuringQuarantineConflicts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		boundary, kube := boundaryFixture()
		binding := boundaryBinding("team")
		job := jobName(binding)
		original, err := boundary.Reserve(t.Context(), binding, job)
		if err != nil {
			t.Fatal(err)
		}
		node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		tampered := binding
		tampered.UnitID = "other-unit"
		raw, err := json.Marshal(tampered)
		if err != nil {
			t.Fatal(err)
		}
		node.Annotations[bindingAnnotation] = string(raw)
		if _, err = kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		patches := 0
		kube.PrependReactor("patch", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
			patches++
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, "node", errors.New("changed"))
		})
		l, _, _, _ := fixture()
		l.Kube = kube
		if err := l.Sync(t.Context()); err == nil || blocking(err) != nil || patches == 0 {
			t.Fatalf("suspect node was not reported without blocking healthy claims: patches=%d err=%v", patches, err)
		}
		if _, err := boundary.Reserve(t.Context(), binding, job); !errors.Is(err, ErrNoDedicatedNode) {
			t.Fatalf("resumed a suspect reservation while quarantine conflicted: %v", err)
		}
		node, err = kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		raw, err = json.Marshal(binding)
		if err != nil {
			t.Fatal(err)
		}
		node.Annotations[bindingAnnotation] = string(raw)
		if _, err = kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		resumed, err := boundary.Reserve(t.Context(), binding, job)
		if err != nil || resumed != original {
			t.Fatalf("repaired reservation did not resume: %+v %v", resumed, err)
		}
	})
}

func TestQuarantineRetryStaysWithinOriginalNodeAndTeam(t *testing.T) {
	for _, change := range []string{"replacement", "protected team", "erased ownership", "protected team during patch"} {
		t.Run(change, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, kube := boundaryFixture()
				node := reservedNode(t, "tampered", boundaryBinding("team"), "garbage")
				if _, err := kube.CoreV1().Nodes().Create(t.Context(), node, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
				patches := 0
				kube.PrependReactor("patch", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
					patches++
					if patches > 1 {
						return false, nil, nil
					}
					changed := node.DeepCopy()
					switch change {
					case "replacement":
						changed.UID = "replacement-uid"
					case "protected team", "protected team during patch":
						changed.Labels[TeamNodeLabel] = TeamNodeValue("cloud", "other")
					case "erased ownership":
						changed.Annotations = nil
					}
					if err := kube.Tracker().Update(corev1.SchemeGroupVersion.WithResource("nodes"), changed, ""); err != nil {
						t.Fatal(err)
					}
					if change == "protected team during patch" {
						return false, nil, nil
					}
					return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, node.Name, errors.New("changed"))
				})
				if err := quarantineNode(t.Context(), kube, node, "cloud", "reservation is malformed or does not match its award"); err == nil || change != "protected team during patch" && blocking(err) != nil {
					t.Fatalf("changed node was not reported as a contained fault: %v", err)
				}
				got, err := kube.CoreV1().Nodes().Get(t.Context(), node.Name, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if patches != 1 || quarantined(*got) || got.Spec.Unschedulable {
					t.Fatalf("quarantine crossed changed identity: patches=%d node=%+v", patches, got)
				}
			})
		})
	}
}
