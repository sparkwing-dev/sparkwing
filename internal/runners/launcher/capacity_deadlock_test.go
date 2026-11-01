package launcher

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

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
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: k8stypes.UID("uid-" + name), ResourceVersion: "1", OwnerReferences: []metav1.OwnerReference{{APIVersion: "karpenter.sh/v1", Kind: "NodeClaim", Name: "claim-" + name, UID: k8stypes.UID("claim-uid-" + name)}}, Labels: map[string]string{CloudClassLabel: "2", PoolNamespaceLabel: "cloud", WarmNamespaceLabel: "warm", corev1.LabelHostname: name}}, Spec: corev1.NodeSpec{ProviderID: provider, Taints: []corev1.Taint{{Key: DedicatedTaint, Value: "true", Effect: corev1.TaintEffectNoSchedule}}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
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
			pod.Status.Phase = corev1.PodSucceeded
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
			l := &Launcher{Kube: p.kube, Config: CloudJobConfig{Namespace: "cloud", RunnerImage: "registry/runner@sha256:" + strings.Repeat("a", 64), CPUCeiling: 2, MemoryCeiling: 8 << 30}}
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

// Capacity requests repel every other team's Pods in the namespace, never their own team's.
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

// A finished request no longer pends, so the held award must request again for
// Karpenter to see demand; a live request is left alone.
func TestStoppedCapacityRequestIsReplaced(t *testing.T) {
	for name, stopped := range map[string]bool{"stopped": true, "running": false} {
		t.Run(name, func(t *testing.T) {
			l, kube, _, control := fixture()
			if err := l.requestCapacity(t.Context(), *control.award); err != nil {
				t.Fatal(err)
			}
			pod := capacityPods(t, kube)[0]
			pod.UID, pod.Spec.NodeName, pod.Status.Phase = "first", "spent-node", corev1.PodRunning
			if stopped {
				pod.Status.Phase = corev1.PodSucceeded
			}
			if _, err := kube.CoreV1().Pods("cloud").Update(t.Context(), &pod, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := l.requestCapacity(t.Context(), *control.award); err != nil {
				t.Fatal(err)
			}
			left := capacityPods(t, kube)
			if stopped != (len(left) == 0) {
				t.Fatalf("stopped=%t left=%d", stopped, len(left))
			}
			if err := l.requestCapacity(t.Context(), *control.award); err != nil {
				t.Fatal(err)
			}
			again := capacityPods(t, kube)
			if len(again) != 1 || stopped && again[0].Spec.NodeName != "" {
				t.Fatalf("held award did not express fresh demand: %+v", again)
			}
		})
	}
}
