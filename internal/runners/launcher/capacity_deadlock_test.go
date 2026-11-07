package launcher

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// hack: a stand-in for the scheduler and Karpenter. Like the scheduler, it ignores
// stopped Pods for pod anti-affinity, and it adds a fresh node when no node fits.
type pool struct {
	t     *testing.T
	b     *KubernetesBoundary
	kube  *fake.Clientset
	nodes int
}

func emptyPool(t *testing.T) *pool {
	b, kube := boundaryFixture()
	if err := kube.CoreV1().Nodes().Delete(t.Context(), "node", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	return &pool{t: t, b: b, kube: kube}
}

func (p *pool) provision() *corev1.Node {
	p.t.Helper()
	p.nodes++
	name := fmt.Sprintf("fresh-%d", p.nodes)
	provider := "aws:///zone/i-" + name
	claim := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.sh/v1", "kind": "NodeClaim",
		"metadata": map[string]any{"name": "claim-" + name, "uid": "claim-uid-" + name, "labels": map[string]any{CloudClassLabel: "2", PoolNamespaceLabel: "cloud", WarmNamespaceLabel: "warm", "karpenter.sh/nodepool": "pool"}, "ownerReferences": []any{map[string]any{"apiVersion": "karpenter.sh/v1", "kind": "NodePool", "name": "pool", "uid": "pool-uid"}}},
		"status":   map[string]any{"nodeName": name, "providerID": provider},
	}}
	if _, err := p.b.Karpenter.Resource(nodeClaims).Create(p.t.Context(), claim, metav1.CreateOptions{}); err != nil {
		p.t.Fatal(err)
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: k8stypes.UID("uid-" + name), ResourceVersion: "1", OwnerReferences: []metav1.OwnerReference{{APIVersion: "karpenter.sh/v1", Kind: "NodeClaim", Name: "claim-" + name, UID: k8stypes.UID("claim-uid-" + name)}}, Labels: map[string]string{CloudClassLabel: "2", PoolNamespaceLabel: "cloud", WarmNamespaceLabel: "warm", NodeClassLabel: "sparkwing-jobs", corev1.LabelHostname: name}}, Spec: corev1.NodeSpec{ProviderID: provider, Taints: []corev1.Taint{{Key: DedicatedTaint, Value: "true", Effect: corev1.TaintEffectNoSchedule}}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	created, err := p.kube.CoreV1().Nodes().Create(p.t.Context(), node, metav1.CreateOptions{})
	if err != nil {
		p.t.Fatal(err)
	}
	return created
}

func (p *pool) pods() []corev1.Pod {
	p.t.Helper()
	pods, err := p.kube.CoreV1().Pods("").List(p.t.Context(), metav1.ListOptions{})
	if err != nil {
		p.t.Fatal(err)
	}
	return pods.Items
}

func antiAffine(t *testing.T, pod, other corev1.Pod) bool {
	t.Helper()
	if pod.Spec.Affinity == nil || pod.Spec.Affinity.PodAntiAffinity == nil {
		return false
	}
	for _, term := range pod.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
		if term.TopologyKey != corev1.LabelHostname || other.Namespace != pod.Namespace {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(term.LabelSelector)
		if err != nil {
			t.Fatal(err)
		}
		if selector.Matches(labels.Set(other.Labels)) {
			return true
		}
	}
	return false
}

func (p *pool) fits(pod corev1.Pod, node corev1.Node, resident []corev1.Pod) bool {
	if !nodeReady(node) {
		return false
	}
	for key, value := range pod.Spec.NodeSelector {
		if node.Labels[key] != value {
			return false
		}
	}
	for _, taint := range node.Spec.Taints {
		if !slices.ContainsFunc(pod.Spec.Tolerations, func(t corev1.Toleration) bool {
			return t.Key == taint.Key && t.Effect == taint.Effect && (t.Operator == corev1.TolerationOpExists || t.Value == taint.Value)
		}) {
			return false
		}
	}
	terms := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if !slices.ContainsFunc(terms, func(term corev1.NodeSelectorTerm) bool {
		for _, r := range term.MatchExpressions {
			value, has := node.Labels[r.Key]
			if r.Operator == corev1.NodeSelectorOpDoesNotExist && has || r.Operator == corev1.NodeSelectorOpIn && (!has || !slices.Contains(r.Values, value)) {
				return false
			}
		}
		return true
	}) {
		return false
	}
	for _, other := range resident {
		if other.Spec.NodeName == node.Name && !podStopped(other) && (antiAffine(p.t, pod, other) || antiAffine(p.t, other, pod)) {
			return false
		}
	}
	return true
}

// hack: serial stops each request before the next is placed, as when one team's
// request finishes first.
func (p *pool) schedule(serial bool) {
	p.t.Helper()
	for _, pod := range p.pods() {
		if pod.Labels[capacityLabel] != "true" || pod.Spec.NodeName != "" {
			continue
		}
		nodes, err := p.kube.CoreV1().Nodes().List(p.t.Context(), metav1.ListOptions{})
		if err != nil {
			p.t.Fatal(err)
		}
		resident := p.pods()
		target := ""
		for _, node := range nodes.Items {
			if p.fits(pod, node, resident) {
				target = node.Name
				break
			}
		}
		if target == "" {
			target = p.provision().Name
		}
		pod.Spec.NodeName = target
		if serial {
			pod.Status.Phase = corev1.PodSucceeded
		}
		if _, err := p.kube.CoreV1().Pods(pod.Namespace).Update(p.t.Context(), &pod, metav1.UpdateOptions{}); err != nil {
			p.t.Fatal(err)
		}
	}
	for _, pod := range p.pods() {
		if pod.Labels[capacityLabel] == "true" && pod.Spec.NodeName != "" && pod.Status.Phase == "" {
			pod.Status.Phase = corev1.PodRunning
			if _, err := p.kube.CoreV1().Pods(pod.Namespace).Update(p.t.Context(), &pod, metav1.UpdateOptions{}); err != nil {
				p.t.Fatal(err)
			}
		}
	}
}

func teamAward(team string) CloudJobAward {
	binding := boundaryBinding(team)
	binding.RunID = runspec.RunID("run-" + team)
	return CloudJobAward{Binding: binding, CPUCores: 2, Memory: 1 << 30}
}

// Grid cell R21C01: two teams asked for class-2 work at once on an empty shared
// pool, both capacity requests ran on one fresh node, and each team's finished
// request kept the other from reserving it, so neither Job ever started.
func TestTwoTeamsOnAnEmptyPoolEachGetTheirOwnNode(t *testing.T) {
	for name, serial := range map[string]bool{"concurrent": false, "one after the other": true} {
		t.Run(name, func(t *testing.T) {
			p := emptyPool(t)
			l := &Launcher{Kube: p.kube, Boundary: p.b, Config: CloudJobConfig{Namespace: "cloud", RunnerImage: "registry/runner@sha256:" + strings.Repeat("a", 64), CPUCeiling: 2, MemoryCeiling: 8 << 30}}
			teams := []string{"stg-qa", "korey-gam"}
			var mu sync.Mutex
			reserved := map[string]CloudNodeReservation{}
			for round := 0; round < 6 && len(reserved) < len(teams); round++ {
				var wg sync.WaitGroup
				errs := make([]error, len(teams))
				for i, team := range teams {
					mu.Lock()
					_, done := reserved[team]
					mu.Unlock()
					if done {
						continue
					}
					wg.Go(func() {
						award := teamAward(team)
						r, err := p.b.Reserve(context.WithoutCancel(t.Context()), award.Binding, "job-"+team, CloudPlacement{RequestedClass: "2"})
						if errors.Is(err, ErrNoDedicatedNode) {
							errs[i] = l.requestCapacity(t.Context(), award)
							return
						}
						errs[i] = err
						if err == nil {
							mu.Lock()
							reserved[team] = r
							mu.Unlock()
						}
					})
				}
				wg.Wait()
				if err := errors.Join(errs...); err != nil {
					t.Fatal(err)
				}
				p.schedule(serial)
			}
			if len(reserved) != len(teams) {
				t.Fatalf("teams waited forever: reserved=%v pods=%v", reserved, p.pods())
			}
			if reserved[teams[0]].NodeUID == reserved[teams[1]].NodeUID {
				t.Fatalf("two teams share node %s", reserved[teams[0]].NodeName)
			}
			if p.nodes != len(teams) {
				t.Fatalf("provisioned %d nodes for %d teams", p.nodes, len(teams))
			}
			for team, r := range reserved {
				node, err := p.kube.CoreV1().Nodes().Get(t.Context(), r.NodeName, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if node.Labels[TeamNodeLabel] != TeamNodeValue("cloud", team) || node.Labels[usedTeamLabel] != TeamNodeValue("cloud", team) {
					t.Fatalf("node %s bound to %v, want %s", node.Name, node.Labels, team)
				}
			}
			if _, err := p.b.Reserve(t.Context(), teamAward("third").Binding, "job-third", CloudPlacement{RequestedClass: "2"}); !errors.Is(err, ErrNoDedicatedNode) {
				t.Fatalf("a bound node was handed to a third team: %v", err)
			}
		})
	}
}

func TestCapacityRequestRepelsOtherTeamsPods(t *testing.T) {
	cfg := CloudJobConfig{Namespace: "cloud", CPUCeiling: 2, MemoryCeiling: 8 << 30}
	request := func(team string) corev1.Pod {
		pod, err := capacityPod(cfg, demandFor(teamAward(team)))
		if err != nil {
			t.Fatal(err)
		}
		return *pod
	}
	own, other := request("team"), request("other")
	for _, term := range own.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
		if len(term.Namespaces) != 0 || term.NamespaceSelector != nil {
			t.Fatalf("anti-affinity crosses the job namespace: %+v", term)
		}
	}
	job := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "cloud", Labels: map[string]string{"app": "sparkwing-job", TeamLabel: TeamNodeValue("cloud", "other"), JobLabel: "sw-job"}}}
	unlabelled := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "cloud", Labels: map[string]string{"app": "other"}}}
	if !antiAffine(t, own, other) || !antiAffine(t, own, job) || !antiAffine(t, other, own) {
		t.Fatal("capacity request may share a node with another team's Pod")
	}
	sameTeam := request("team")
	sameTeam.Name = "sibling"
	if antiAffine(t, own, sameTeam) || antiAffine(t, own, unlabelled) {
		t.Fatal("capacity request repels its own team or unrelated Pods")
	}
}

func TestCapacityRequestIsReplacedOnlyAfterDisplacement(t *testing.T) {
	for _, phase := range []corev1.PodPhase{corev1.PodRunning, corev1.PodSucceeded} {
		for _, state := range []string{"foreign pod", "foreign team binding", "foreign team use", "foreign run binding", "idle", "own pod"} {
			t.Run(string(phase)+"/"+state, func(t *testing.T) {
				l, kube, _, control := fixture()
				node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "spent-node", Labels: map[string]string{}}}
				switch state {
				case "foreign team binding":
					node.Labels[TeamNodeLabel] = TeamNodeValue("cloud", "other")
				case "foreign team use":
					node.Labels[usedTeamLabel] = TeamNodeValue("cloud", "other")
				case "foreign run binding":
					node.Labels[usedRunLabel] = runNodeValue("cloud", "other")
				case "foreign pod", "own pod":
					team := "other"
					if state == "own pod" {
						team = "team"
					}
					other := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "cloud", Labels: map[string]string{TeamLabel: TeamNodeValue("cloud", team)}}, Spec: corev1.PodSpec{NodeName: node.Name}}
					if _, err := kube.CoreV1().Pods("cloud").Create(t.Context(), other, metav1.CreateOptions{}); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := kube.CoreV1().Nodes().Create(t.Context(), node, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
				if err := l.requestCapacity(t.Context(), *control.award); err != nil {
					t.Fatal(err)
				}
				pod := capacityPods(t, kube)[0]
				pod.UID, pod.Spec.NodeName, pod.Status.Phase = "first", node.Name, phase
				if _, err := kube.CoreV1().Pods("cloud").Update(t.Context(), &pod, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
				replace := strings.HasPrefix(state, "foreign")
				if err := l.requestCapacity(t.Context(), *control.award); err != nil {
					t.Fatal(err)
				}
				if left := capacityPods(t, kube); replace != (len(left) == 0) {
					t.Fatalf("replace=%t left=%d", replace, len(left))
				}
				if err := l.requestCapacity(t.Context(), *control.award); err != nil {
					t.Fatal(err)
				}
				again := capacityPods(t, kube)
				if len(again) != 1 || replace && again[0].Spec.NodeName != "" || !replace && again[0].UID != pod.UID {
					t.Fatalf("held award did not preserve or replace its request: %+v", again)
				}
			})
		}
	}
}

func TestCapacityRequestSleepsWithinItsAwardBudget(t *testing.T) {
	synctest.Test(t, testCapacityRequestSleepsWithinItsAwardBudget)
}

func testCapacityRequestSleepsWithinItsAwardBudget(t *testing.T) {
	l, _, _, control := fixture()
	control.award.Binding.AttemptDeadline = time.Now().Add(time.Minute)
	before := time.Now()
	pod, err := capacityPod(l.Config, demandFor(*control.award))
	if err != nil {
		t.Fatal(err)
	}
	c := pod.Spec.Containers[0]
	if !slices.Equal(c.Command, capacityCommand) || len(c.Args) != 1 {
		t.Fatalf("capacity request does not hold its node with a fixed script and its budget as an argument: %+v", c)
	}
	seconds, err := strconv.ParseFloat(c.Args[0], 64)
	deadline := demandDeadline(control.award.Binding)
	if err != nil || seconds <= 0 || seconds != deadline.Sub(before).Seconds() {
		t.Fatalf("sleep budget %q is outside the award deadline: %v", c.Args[0], err)
	}
}

func TestTerminatingOldCapacityRequestWaitsForRemoval(t *testing.T) {
	l, kube, _, control := fixture()
	old, err := capacityPod(l.Config, demandFor(*control.award))
	if err != nil {
		t.Fatal(err)
	}
	old.UID, old.DeletionTimestamp = "old", ptr(metav1.Now())
	old.Finalizers = []string{"test/termination"}
	old.Spec.Affinity.PodAntiAffinity = nil
	old.Spec.Containers[0].Command, old.Spec.Containers[0].Args = []string{JobBinary}, []string{"version"}
	if _, err := kube.CoreV1().Pods("cloud").Create(t.Context(), old, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	kube.PrependReactor("delete", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		t.Fatal("adoption tried to delete a terminating old request again")
		return true, nil, nil
	})
	if err := l.requestCapacity(t.Context(), *control.award); err != nil {
		t.Fatalf("AlreadyExists adoption rejected a terminating old request: %v", err)
	}
	if err := kube.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), "cloud", old.Name); err != nil {
		t.Fatal(err)
	}
	if err := l.requestCapacity(t.Context(), *control.award); err != nil {
		t.Fatal(err)
	}
	pods := capacityPods(t, kube)
	if len(pods) != 1 || pods[0].DeletionTimestamp != nil || pods[0].Spec.Affinity.PodAntiAffinity == nil || !slices.Equal(pods[0].Spec.Containers[0].Command, capacityCommand) {
		t.Fatalf("old request was not replaced after removal: %+v", pods)
	}
}

func capacityLauncherFixture(t *testing.T) (*Launcher, *fake.Clientset, *KubernetesBoundary, *controlFake) {
	t.Helper()
	l, kube, _, control := fixture()
	control.award.CPUCores = 2
	b, nodes := boundaryFixture()
	node, err := nodes.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kube.CoreV1().Nodes().Create(t.Context(), node, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	b.Kube, l.Boundary = kube, b
	return l, kube, b, control
}

func TestIncompatibleCapacityNodeDoesNotChurnRequests(t *testing.T) {
	l, kube, b, control := capacityLauncherFixture(t)
	pool, err := b.Karpenter.Resource(nodePools).Get(t.Context(), "pool", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pool.SetName("incompatible")
	pool.SetUID("incompatible-uid")
	if err := unstructured.SetNestedField(pool.Object, "unhardened", "spec", "template", "spec", "nodeClassRef", "name"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Karpenter.Resource(nodePools).Create(t.Context(), pool, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	claim, err := b.Karpenter.Resource(nodeClaims).Get(t.Context(), "claim", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	claim.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "karpenter.sh/v1", Kind: "NodePool", Name: pool.GetName(), UID: pool.GetUID()}})
	claimLabels := claim.GetLabels()
	claimLabels["karpenter.sh/nodepool"] = pool.GetName()
	claim.SetLabels(claimLabels)
	if _, err := b.Karpenter.Resource(nodeClaims).Update(t.Context(), claim, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.LaunchOne(t.Context()); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatal(err)
	}
	pod := capacityPods(t, kube)[0]
	pod.UID, pod.Spec.NodeName, pod.Status.Phase = "original", "node", corev1.PodSucceeded
	if _, err := kube.CoreV1().Pods("cloud").Update(t.Context(), &pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	control.award = nil
	kube.PrependReactor("delete", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		t.Fatal("incompatible capacity node triggered request recreation")
		return true, nil, nil
	})
	for range 3 {
		if _, err := l.LaunchOne(t.Context()); err != nil {
			t.Fatal(err)
		}
		if pods := capacityPods(t, kube); len(pods) != 1 || pods[0].UID != pod.UID || len(l.waitingAwards()) != 1 {
			t.Fatalf("incompatible capacity churned: %+v", pods)
		}
	}
	if jobs, err := kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{}); err != nil || len(jobs.Items) != 0 {
		t.Fatalf("incompatible node launched a Job: %+v %v", jobs, err)
	}
}

func TestCapacityArrivalBetweenReserveAndConsumeDefersJobUntilPodLeaves(t *testing.T) {
	l, kube, boundary, control := capacityLauncherFixture(t)
	award := *control.award
	otherAward := award
	otherAward.Binding = teamAward("other").Binding
	otherLauncher := &Launcher{Kube: kube, Control: &controlFake{award: &otherAward}, Boundary: boundary, Config: l.Config}
	other, err := capacityPod(l.Config, demandFor(otherAward))
	if err != nil {
		t.Fatal(err)
	}
	other.Spec.NodeName, other.UID, other.Status.Phase = "node", "other-uid", corev1.PodRunning
	injected := false
	kube.PrependReactor("update", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
		node := action.(ktesting.UpdateAction).GetObject().(*corev1.Node)
		if !injected && reservationEvidence(node) {
			injected = true
			if err := kube.Tracker().Add(other); err != nil {
				t.Fatal(err)
			}
		}
		return false, nil, nil
	})
	var first CloudNodeReservation
	for pass := range 2 {
		if _, err := l.launch(t.Context(), &award); !errors.Is(err, ErrNoDedicatedNode) {
			t.Fatalf("pass %d: foreign capacity did not defer publication: %v", pass, err)
		}
		node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		held, binding, ok := nodeReservation(node)
		if !ok || !sameBinding(binding, award.Binding) || pass > 0 && held != first {
			t.Fatalf("reservation was lost or rebound during replay: %+v", node)
		}
		first = held
		if jobs, err := kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{}); err != nil || len(jobs.Items) != 0 {
			t.Fatalf("Job was published with foreign capacity present: %+v %v", jobs, err)
		}
		if _, err := kube.CoreV1().Pods("cloud").Get(t.Context(), other.Name, metav1.GetOptions{}); err != nil {
			t.Fatalf("another team's request was consumed: %v", err)
		}
	}
	deleted := false
	kube.PrependReactor("delete", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		deletion := action.(ktesting.DeleteAction)
		if deletion.GetName() == other.Name && !deleted {
			preconditions := deletion.GetDeleteOptions().Preconditions
			if preconditions == nil || preconditions.UID == nil || *preconditions.UID != other.UID {
				t.Fatal("displaced request deletion lost its UID fence")
			}
			deleted = true
		}
		return false, nil, nil
	})
	if _, err := otherLauncher.launch(t.Context(), &otherAward); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatalf("displaced award did not wait for eligible capacity: %v", err)
	}
	if !deleted {
		t.Fatal("owning launcher left its Running request on another team's reserved node")
	}
	if _, err := otherLauncher.launch(t.Context(), &otherAward); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatalf("displaced award did not recreate demand: %v", err)
	}
	replacement, err := kube.CoreV1().Pods("cloud").Get(t.Context(), other.Name, metav1.GetOptions{})
	if err != nil || replacement.Spec.NodeName != "" {
		t.Fatalf("displaced request did not recreate Pending demand: %+v %v", replacement, err)
	}
	p := &pool{t: t, b: boundary, kube: kube}
	p.schedule(false)
	if _, err := l.launch(t.Context(), &award); err != nil {
		t.Fatalf("reservation did not resume after the foreign Pod left: %v", err)
	}
	if _, err := otherLauncher.launch(t.Context(), &otherAward); err != nil {
		t.Fatalf("displaced award did not start on eligible capacity: %v", err)
	}
	jobs, err := kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 2 || len(capacityPods(t, kube)) != 0 {
		t.Fatalf("both teams did not publish Jobs and consume their demand: %+v %v", jobs, err)
	}
	wantNodes := map[string]string{jobName(award.Binding): "node", jobName(otherAward.Binding): "fresh-1"}
	for _, job := range jobs.Items {
		if job.Spec.Suspend == nil || *job.Spec.Suspend || job.Spec.Template.Spec.NodeName != wantNodes[job.Name] {
			t.Fatalf("Job did not start on its team's eligible node: %+v", job)
		}
	}
}
