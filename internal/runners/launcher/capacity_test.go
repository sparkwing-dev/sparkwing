package launcher

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func capacityPods(t *testing.T, kube *fake.Clientset) []corev1.Pod {
	t.Helper()
	pods, err := kube.CoreV1().Pods("cloud").List(t.Context(), metav1.ListOptions{LabelSelector: capacityLabel + "=true"})
	if err != nil {
		t.Fatal(err)
	}
	return pods.Items
}

func capacityMutations() map[string]func(*corev1.Pod) {
	return map[string]func(*corev1.Pod){
		"image": func(p *corev1.Pod) { p.Spec.Containers[0].Image = "registry/other@sha256:" + strings.Repeat("c", 64) },
		"sidecar": func(p *corev1.Pod) {
			p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "sidecar", Image: "other"})
		},
		"other_team":   func(p *corev1.Pod) { p.Spec.NodeSelector[TeamNodeLabel] = TeamNodeValue("cloud", "other") },
		"other_demand": func(p *corev1.Pod) { p.Annotations[demandAnnotation] = `{"binding":{}}` },
		"coordinated_size": func(p *corev1.Pod) {
			var d capacityDemand
			if err := json.Unmarshal([]byte(p.Annotations[demandAnnotation]), &d); err != nil {
				panic(err)
			}
			d.CPUCores = 0.5
			raw, err := json.Marshal(d)
			if err != nil {
				panic(err)
			}
			p.Annotations[demandAnnotation] = string(raw)
			c := &p.Spec.Containers[0]
			c.Resources.Requests[corev1.ResourceCPU], c.Resources.Limits[corev1.ResourceCPU] = resource.MustParse("500m"), resource.MustParse("500m")
		},
	}
}

func TestLaunchWithoutATeamNodeHoldsTheAwardAndRequestsOneNodePerUnit(t *testing.T) {
	l, kube, boundary, control := fixture()
	boundary.reserveErr = ErrNoDedicatedNode
	if _, err := l.LaunchOne(t.Context()); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatalf("launch without a node: %v", err)
	}
	pods := capacityPods(t, kube)
	if len(pods) != 1 || len(l.waitingAwards()) != 1 {
		t.Fatalf("capacity requests = %d, held awards = %d, want 1 and 1", len(pods), len(l.waitingAwards()))
	}
	spec := pods[0].Spec
	if spec.NodeName != "" || !reflect.DeepEqual(spec.NodeSelector, map[string]string{TeamNodeLabel: TeamNodeValue("cloud", "team")}) || !reflect.DeepEqual(spec.Tolerations, teamTolerations("cloud", "team")) {
		t.Fatalf("request is not confined to the team pool: %+v", spec)
	}
	if spec.ServiceAccountName != ServiceAccount || spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken || len(spec.Volumes) != 0 || len(spec.InitContainers) != 0 || len(spec.Containers) != 1 {
		t.Fatalf("request carries more than a sized placeholder: %+v", spec)
	}
	container := spec.Containers[0]
	want := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1500m"), corev1.ResourceMemory: *resource.NewQuantity(2<<30, resource.BinarySI), corev1.ResourceEphemeralStorage: *resource.NewQuantity(limits.JobScratchBytes, resource.BinarySI)}
	for name, quantity := range want {
		if got := container.Resources.Requests[name]; got.Cmp(quantity) != 0 {
			t.Fatalf("request %s = %s, want %s", name, got.String(), quantity.String())
		}
	}
	if len(container.Env) != 0 || len(container.EnvFrom) != 0 || len(container.VolumeMounts) != 0 || container.Image != l.Config.RunnerImage {
		t.Fatalf("request container = %+v", container)
	}
	first := *control.award
	control.award = nil
	if _, err := l.LaunchOne(t.Context()); err != nil || len(capacityPods(t, kube)) != 1 || control.claims != 2 {
		t.Fatalf("a held award asked for a second node: claims=%d %v", control.claims, err)
	}
	second := first
	second.Binding.UnitID, second.Binding.NodeID = "unit-2", "job-2"
	control.award = &second
	if _, err := l.LaunchOne(t.Context()); !errors.Is(err, ErrNoDedicatedNode) || len(capacityPods(t, kube)) != 2 || len(l.waitingAwards()) != 2 {
		t.Fatalf("a second waiting unit did not ask for its own node: %v", err)
	}
	if jobs, err := kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{}); err != nil || len(jobs.Items) != 0 {
		t.Fatalf("a job started without a reserved node: %v", err)
	}
	boundary.reserveErr, control.award = nil, nil
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs, err := kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 2 || len(capacityPods(t, kube)) != 0 || len(l.waitingAwards()) != 0 {
		t.Fatalf("held awards did not reach their nodes: jobs=%d requests=%d held=%d %v", len(jobs.Items), len(capacityPods(t, kube)), len(l.waitingAwards()), err)
	}
}

func TestCapacityRequestMutationIsRejected(t *testing.T) {
	for name, mutate := range capacityMutations() {
		t.Run(name+"/admitted", func(t *testing.T) {
			l, kube, boundary, _ := fixture()
			boundary.reserveErr = ErrNoDedicatedNode
			kube.PrependReactor("create", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
				if pod := action.(ktesting.CreateAction).GetObject().(*corev1.Pod); pod.Labels[capacityLabel] == "true" {
					mutate(pod)
				}
				return false, nil, nil
			})
			if _, err := l.LaunchOne(t.Context()); err == nil || len(capacityPods(t, kube)) != 0 {
				t.Fatalf("mutated request was accepted: %v", err)
			}
		})
		t.Run(name+"/adopted", func(t *testing.T) {
			l, kube, boundary, control := fixture()
			boundary.reserveErr = ErrNoDedicatedNode
			pod, err := capacityPod(l.Config, demandFor(*control.award))
			if err != nil {
				t.Fatal(err)
			}
			mutate(pod)
			if _, err := kube.CoreV1().Pods("cloud").Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := l.LaunchOne(t.Context()); err == nil || len(capacityPods(t, kube)) != 0 {
				t.Fatalf("mutated request was adopted: %v", err)
			}
		})
		t.Run(name+"/stored", func(t *testing.T) {
			l, kube, boundary, _ := fixture()
			boundary.reserveErr = ErrNoDedicatedNode
			if _, err := l.LaunchOne(t.Context()); !errors.Is(err, ErrNoDedicatedNode) {
				t.Fatal(err)
			}
			pod := capacityPods(t, kube)[0]
			mutate(&pod)
			if _, err := kube.CoreV1().Pods("cloud").Update(t.Context(), &pod, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := l.Sync(t.Context()); err == nil || len(capacityPods(t, kube)) != 0 {
				t.Fatalf("mutated stored request survived: %v", err)
			}
			if len(l.waitingAwards()) != 1 {
				t.Fatal("a rejected request dropped the live award")
			}
		})
	}
}

func TestCapacityRequestCarriesNoAwardCredentialProperty(t *testing.T) {
	l, _, _, control := fixture()
	property := func(secret [24]byte, team string) bool {
		if team == "" {
			return true
		}
		award := *control.award
		award.Bootstrap = []byte("swk_" + hex.EncodeToString(secret[:]))
		award.Binding.Team = team
		pod, err := capacityPod(l.Config, demandFor(award))
		if err != nil {
			t.Log(err)
			return false
		}
		raw, err := json.Marshal(pod)
		return err == nil && !bytes.Contains(raw, award.Bootstrap) && pod.Spec.NodeSelector[TeamNodeLabel] == TeamNodeValue("cloud", team) && len(pod.Spec.Volumes) == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
}

func TestCapacityRequestLivesExactlyAsLongAsItsAward(t *testing.T) {
	l, kube, boundary, control := fixture()
	boundary.reserveErr = ErrNoDedicatedNode
	if _, err := l.LaunchOne(t.Context()); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatal(err)
	}
	pod := capacityPods(t, kube)[0]
	pod.Spec.NodeName = "ready"
	if _, err := kube.CoreV1().Pods("cloud").Update(t.Context(), &pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "ready", UID: "ready-uid", Labels: map[string]string{TeamNodeLabel: TeamNodeValue("cloud", "team")}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	if _, err := kube.CoreV1().Nodes().Create(t.Context(), node, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	var observed []CloudJobObservation
	ended := false
	decide := awardedStates(*control.award, func(CloudJobBinding) bool { return ended })
	l.Control = publicationControl{CloudControl: control, sync: func(obs []CloudJobObservation) ([]CloudJobState, error) {
		observed = append(observed, obs...)
		return decide(obs)
	}}
	if err := l.Sync(t.Context()); err != nil || len(capacityPods(t, kube)) != 1 {
		t.Fatalf("a Ready node lost its request before an award reserved it: %v", err)
	}
	if len(observed) != 1 || observed[0].Unheld || !sameBinding(observed[0].Binding, control.award.Binding) || observed[0].ReservationID != capacityObservedID+string(pod.UID) {
		t.Fatalf("held demand was not renewed through its award: %+v", observed)
	}
	restarted := &Launcher{Kube: l.Kube, Control: l.Control, Config: l.Config, Boundary: l.Boundary}
	observed = nil
	if err := restarted.Sync(t.Context()); err != nil || len(capacityPods(t, kube)) != 1 || len(observed) != 1 || !observed[0].Unheld {
		t.Fatalf("another launcher renewed an award it does not hold: %+v %v", observed, err)
	}
	ended = true
	if err := l.Sync(t.Context()); err != nil || len(capacityPods(t, kube)) != 0 || len(l.waitingAwards()) != 0 {
		t.Fatalf("demand outlived its cancelled award: %v", err)
	}
}

func TestExpiredCapacityRequestIsRemovedWithoutTheController(t *testing.T) {
	l, kube, _, control := fixture()
	award := *control.award
	award.Binding.OwnerDeadline = time.Now().Add(-time.Minute)
	pod, err := capacityPod(l.Config, demandFor(award))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kube.CoreV1().Pods("cloud").Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := l.Sync(t.Context()); err != nil || len(capacityPods(t, kube)) != 0 {
		t.Fatalf("expired demand survived: %v", err)
	}
}

func TestReservationConsumesOnlyItsTeamsCapacityRequests(t *testing.T) {
	cfg := CloudJobConfig{Namespace: "cloud", RunnerImage: "registry/runner@sha256:" + string(bytes.Repeat([]byte("a"), 64)), CPUCeiling: 4, MemoryCeiling: 8 << 30}
	request := func(team, namespace string) *corev1.Pod {
		binding := boundaryBinding(team)
		pod, err := capacityPod(cfg, capacityDemand{Binding: binding, CPUCores: 1, Memory: 1 << 30})
		if err != nil {
			t.Fatal(err)
		}
		pod.Namespace, pod.Spec.NodeName, pod.UID = namespace, "node", "request-uid"
		return pod
	}
	for name, test := range map[string]struct {
		pod     *corev1.Pod
		reserve bool
	}{
		"own team":        {request("team", "cloud"), true},
		"other team":      {request("other", "cloud"), false},
		"other namespace": {request("team", "elsewhere"), false},
		"unlabelled": {func() *corev1.Pod {
			pod := request("team", "cloud")
			delete(pod.Labels, capacityLabel)
			return pod
		}(), false},
	} {
		t.Run(name, func(t *testing.T) {
			b, kube := boundaryFixture()
			if _, err := kube.CoreV1().Pods(test.pod.Namespace).Create(t.Context(), test.pod, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			_, _, err := b.Reserve(t.Context(), boundaryBinding("team"), "job")
			left, listErr := kube.CoreV1().Pods(test.pod.Namespace).List(t.Context(), metav1.ListOptions{})
			if listErr != nil {
				t.Fatal(listErr)
			}
			if test.reserve && (err != nil || len(left.Items) != 0) {
				t.Fatalf("own request was not handed to the reservation: left=%d %v", len(left.Items), err)
			}
			if !test.reserve && (!errors.Is(err, ErrNoDedicatedNode) || len(left.Items) != 1) {
				t.Fatalf("a foreign Pod was consumed or ignored: left=%d %v", len(left.Items), err)
			}
		})
	}
}

func TestUnheldObservationDoesNotRenewTheAward(t *testing.T) {
	unit := wireAward(capability.UnitNode)
	unit.Capability = ""
	binding, err := cloudBinding(unit, capability.DeliveryBootstrap)
	if err != nil {
		t.Fatal(err)
	}
	for _, unheld := range []bool{false, true} {
		var preparing []capability.LeaseRef
		control, err := NewControllerControl(launcherWireFake{sync: func(_ context.Context, request controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
			preparing = request.Preparing
			return controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{unit}}, nil
		}}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := control.Sync(t.Context(), []CloudJobObservation{{Binding: binding, JobName: jobName(binding), NodeUID: "pod-uid", ReservationID: capacityObservedID + "pod-uid", Unheld: unheld}}); err != nil {
			t.Fatal(err)
		}
		if renewed := len(preparing) == 1; renewed == unheld {
			t.Fatalf("unheld=%t renewed=%t", unheld, renewed)
		}
	}
}

func awardedStates(award CloudJobAward, ended func(CloudJobBinding) bool) func([]CloudJobObservation) ([]CloudJobState, error) {
	return func(obs []CloudJobObservation) ([]CloudJobState, error) {
		states := make([]CloudJobState, len(obs))
		for i, o := range obs {
			states[i] = CloudJobState{Binding: o.Binding, LeaseExpiresAt: time.Now().Add(time.Hour), Delete: ended(o.Binding), Resources: &match.Resources{Cores: award.CPUCores, MemoryBytes: award.Memory}}
		}
		return states, nil
	}
}

func TestStaleCapacityCleanupSparesTheNewerAward(t *testing.T) {
	for _, path := range []string{"release", "retirement"} {
		t.Run(path, func(t *testing.T) {
			l1, kube, boundary, control := fixture()
			boundary.reserveErr = ErrNoDedicatedNode
			g1 := *control.award
			if _, err := l1.LaunchOne(t.Context()); !errors.Is(err, ErrNoDedicatedNode) {
				t.Fatal(err)
			}
			g2 := g1
			g2.Binding.Generation, g2.Binding.Holder = 2, "holder-2"
			l2 := &Launcher{Kube: kube, Control: &controlFake{award: &g2}, Config: l1.Config, Boundary: boundary}
			resume := func() {
				if _, err := l2.LaunchOne(t.Context()); !errors.Is(err, ErrNoDedicatedNode) {
					t.Error(err)
				}
			}
			switch path {
			case "release":
				l1.Control = preparationControl{CloudControl: &controlFake{}, prepare: func(context.Context, []capability.LeaseRef) (time.Time, error) {
					resume()
					return time.Time{}, &apierr.Error{Code: apierr.LockHeld, Message: "award exchanged"}
				}}
				if _, err := l1.LaunchOne(t.Context()); err == nil {
					t.Fatal("a lapsed award was placed")
				}
			case "retirement":
				decide := awardedStates(g1, func(b CloudJobBinding) bool { return b.Generation == 1 })
				l1.Control = publicationControl{CloudControl: control, sync: func(obs []CloudJobObservation) ([]CloudJobState, error) {
					resume()
					return decide(obs)
				}}
				if err := l1.Sync(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			pods := capacityPods(t, kube)
			if len(pods) != 1 || pods[0].Name != capacityName(g2.Binding) || pods[0].Annotations[demandAnnotation] == "" {
				t.Fatalf("G1 cleanup removed G2's request: %d requests", len(pods))
			}
			if len(l1.waitingAwards()) != 0 || len(l2.waitingAwards()) != 1 {
				t.Fatalf("held awards: L1=%d L2=%d", len(l1.waitingAwards()), len(l2.waitingAwards()))
			}
			if err := l2.verifyCapacityPod(&pods[0], demandFor(g2)); err != nil {
				t.Fatalf("G2's request no longer matches its award: %v", err)
			}
		})
	}
}

func TestCapacityDemandIsAuthenticatedOutsideTheAdmittedPod(t *testing.T) {
	for name, resources := range map[string]*match.Resources{"awarded": {Cores: 1.5, MemoryBytes: 2 << 30}, "unknown": nil} {
		t.Run(name, func(t *testing.T) {
			l, kube, boundary, control := fixture()
			boundary.reserveErr = ErrNoDedicatedNode
			if _, err := l.LaunchOne(t.Context()); !errors.Is(err, ErrNoDedicatedNode) {
				t.Fatal(err)
			}
			pod := capacityPods(t, kube)[0]
			capacityMutations()["coordinated_size"](&pod)
			if _, err := kube.CoreV1().Pods("cloud").Update(t.Context(), &pod, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			restarted := &Launcher{Kube: kube, Config: l.Config, Boundary: boundary, Control: publicationControl{CloudControl: control, sync: func(obs []CloudJobObservation) ([]CloudJobState, error) {
				states := make([]CloudJobState, len(obs))
				for i, o := range obs {
					states[i] = CloudJobState{Binding: o.Binding, LeaseExpiresAt: time.Now().Add(time.Hour), Resources: resources}
				}
				return states, nil
			}}}
			if err := restarted.Sync(t.Context()); err == nil || len(capacityPods(t, kube)) != 0 {
				t.Fatalf("an unauthenticated demand survived reconciliation: %v", err)
			}
		})
	}
}
