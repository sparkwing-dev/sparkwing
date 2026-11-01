package launcher

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestSharedPoolBindsOnlyOneTeamAndRun(t *testing.T) {
	b, kube := boundaryFixture()
	var wins atomic.Int32
	var wg sync.WaitGroup
	for _, team := range []string{"alpha", "beta"} {
		for _, run := range []string{"one", "two"} {
			wg.Go(func() {
				binding := boundaryBinding(team)
				binding.RunID = runspec.RunID(run)
				if _, err := b.Reserve(t.Context(), binding, jobName(binding), CloudPlacement{RequestedClass: "2"}); err == nil {
					wins.Add(1)
				} else if !errors.Is(err, ErrNoDedicatedNode) {
					t.Error(err)
				}
			})
		}
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("claims won=%d", wins.Load())
	}
	node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	held, binding, ok := nodeReservation(node)
	if !ok || node.Labels[TeamNodeLabel] != TeamNodeValue("cloud", binding.Team) || node.Labels[usedTeamLabel] != node.Labels[TeamNodeLabel] || node.Labels[usedRunLabel] != runNodeValue("cloud", binding.RunID) {
		t.Fatalf("binding not atomic: %+v", node.Labels)
	}
	if err = b.Release(t.Context(), held); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Reserve(t.Context(), binding, "same-run-next-job", CloudPlacement{RequestedClass: "2"}); err != nil {
		t.Fatalf("same run: %v", err)
	}
}

func setSharedClass(t *testing.T, b *KubernetesBoundary, class string) {
	t.Helper()
	node, err := b.Kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Labels[CloudClassLabel] = class
	if _, err = b.Kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	claim, err := b.Karpenter.Resource(nodeClaims).Get(t.Context(), "claim", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	labels := claim.GetLabels()
	labels[CloudClassLabel] = class
	claim.SetLabels(labels)
	if _, err = b.Karpenter.Resource(nodeClaims).Update(t.Context(), claim, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	pool, err := b.Karpenter.Resource(nodePools).Get(t.Context(), "pool", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = unstructured.SetNestedField(pool.Object, class, "spec", "template", "metadata", "labels", CloudClassLabel); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Karpenter.Resource(nodePools).Update(t.Context(), pool, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "warm", Namespace: "warm", UID: "rs-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "sparkwing-cloud-warm-" + class, UID: "deployment-uid", Controller: ptr(true)}}}}
	if _, err = b.Kube.AppsV1().ReplicaSets("warm").Create(t.Context(), rs, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func sharedWarmPod() *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "warm", Namespace: "warm", UID: types.UID("warm-uid"), Labels: map[string]string{WarmPlaceholderLabel: "true"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "warm", UID: "rs-uid", Controller: ptr(true)}}}, Spec: corev1.PodSpec{NodeName: "node", PriorityClassName: WarmPriorityClass, Priority: ptr(int32(-1000))}}
}

func TestSharedPoolUpsizesOnlyIntoIdleWarmNextClass(t *testing.T) {
	for _, test := range []struct {
		name, class   string
		enabled, warm bool
		mutate        func(*corev1.Pod)
		want          bool
	}{
		{name: "warm next size", class: "4", enabled: true, warm: true, want: true},
		{name: "operator off", class: "4", warm: true},
		{name: "cold larger", class: "4", enabled: true},
		{name: "two sizes up", class: "8", enabled: true, warm: true},
		{name: "wrong namespace", class: "4", enabled: true, warm: true, mutate: func(p *corev1.Pod) { p.Namespace = "foreign" }},
		{name: "unowned", class: "4", enabled: true, warm: true, mutate: func(p *corev1.Pod) { p.OwnerReferences = nil }},
		{name: "volumes", class: "4", enabled: true, warm: true, mutate: func(p *corev1.Pod) {
			p.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
			p.Spec.Containers = []corev1.Container{{Name: "pause", VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}}}}
		}},
		{name: "not low priority", class: "4", enabled: true, warm: true, mutate: func(p *corev1.Pod) { p.Spec.Priority = ptr(int32(0)) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			b, kube := boundaryFixture()
			setSharedClass(t, b, test.class)
			if test.warm {
				p := sharedWarmPod()
				if test.mutate != nil {
					test.mutate(p)
				}
				if _, err := kube.CoreV1().Pods(p.Namespace).Create(t.Context(), p, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			r, err := b.Reserve(t.Context(), boundaryBinding("team"), jobName(boundaryBinding("team")), CloudPlacement{RequestedClass: "2", UpsizeEnabled: test.enabled})
			if !test.want {
				if !errors.Is(err, ErrNoDedicatedNode) {
					t.Fatalf("reservation=%+v err=%v", r, err)
				}
				return
			}
			if err != nil || r.RequestedClass != "2" || r.GrantedClass != "4" {
				t.Fatalf("reservation=%+v err=%v", r, err)
			}
			if _, err = kube.CoreV1().Pods("warm").Get(t.Context(), "warm", metav1.GetOptions{}); err == nil {
				t.Fatal("placeholder retained")
			}
			replay, err := b.Reserve(t.Context(), boundaryBinding("team"), jobName(boundaryBinding("team")), CloudPlacement{RequestedClass: "2", UpsizeEnabled: test.enabled})
			if err != nil || replay != r {
				t.Fatalf("replay=%+v err=%v", replay, err)
			}
			if err = b.Release(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			next, err := b.Reserve(t.Context(), boundaryBinding("team"), "same-run-next", CloudPlacement{RequestedClass: "2", UpsizeEnabled: true})
			if err != nil || next.GrantedClass != "4" {
				t.Fatalf("same run lost its warm node: %+v %v", next, err)
			}
			if err = b.Release(t.Context(), next); err != nil {
				t.Fatal(err)
			}
			other := boundaryBinding("team")
			other.RunID = "another-run"
			if _, err = b.Reserve(t.Context(), other, "another-run", CloudPlacement{RequestedClass: "2", UpsizeEnabled: true}); !errors.Is(err, ErrNoDedicatedNode) {
				t.Fatalf("another run reused warm node: %v", err)
			}
		})
	}
}

func TestSharedPoolUpsizedJobKeepsRequestedCgroupBudget(t *testing.T) {
	l, _, _, control := fixture()
	b, _ := boundaryFixture()
	setSharedClass(t, b, "4")
	p := sharedWarmPod()
	if _, err := b.Kube.CoreV1().Pods(p.Namespace).Create(t.Context(), p, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	a := *control.award
	a.CPUCores = 2
	a.Memory = match.CloudSizeMemoryBytes(2)
	a.UpsizeEnabled = true
	l.Config.CPUCeiling = 4
	l.Config.MemoryCeiling = 16 << 30
	r, err := BuildJob(t.Context(), time.Now(), l.Config, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if r.Reservation.GrantedClass != "4" || r.Job.Spec.Template.Spec.Resources.Limits.Cpu().Value() != 2 || r.Job.Spec.Template.Spec.Resources.Limits.Memory().Value() != a.Memory {
		t.Fatalf("upsize changed requested limits: %+v", r.Job.Spec.Template.Spec.Resources)
	}
}

func TestSharedPoolPrefersRequestedClassBeforeWarmUpsize(t *testing.T) {
	b, kube := boundaryFixture()
	// hack: sorting the larger node first exposes selection that fails to prefer the requested class.
	setSharedClass(t, b, "4")
	warm := sharedWarmPod()
	if _, err := kube.CoreV1().Pods(warm.Namespace).Create(t.Context(), warm, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Name = "z-requested"
	node.UID = "requested-uid"
	node.ResourceVersion = "1"
	node.Labels[CloudClassLabel] = "2"
	node.OwnerReferences[0].Name = "requested-claim"
	node.OwnerReferences[0].UID = "requested-claim-uid"
	claim, err := b.Karpenter.Resource(nodeClaims).Get(t.Context(), "claim", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	claim.SetName("requested-claim")
	claim.SetUID("requested-claim-uid")
	labels := claim.GetLabels()
	labels[CloudClassLabel] = "2"
	labels["karpenter.sh/nodepool"] = "requested-pool"
	claim.SetLabels(labels)
	claim.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "karpenter.sh/v1", Kind: "NodePool", Name: "requested-pool", UID: "requested-pool-uid"}})
	if err = unstructured.SetNestedField(claim.Object, node.Name, "status", "nodeName"); err != nil {
		t.Fatal(err)
	}
	pool, err := b.Karpenter.Resource(nodePools).Get(t.Context(), "pool", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pool.SetName("requested-pool")
	pool.SetUID("requested-pool-uid")
	if err = unstructured.SetNestedField(pool.Object, "2", "spec", "template", "metadata", "labels", CloudClassLabel); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Karpenter.Resource(nodePools).Create(t.Context(), pool, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Karpenter.Resource(nodeClaims).Create(t.Context(), claim, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = kube.CoreV1().Nodes().Create(t.Context(), node, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	r, err := b.Reserve(t.Context(), boundaryBinding("team"), jobName(boundaryBinding("team")), CloudPlacement{RequestedClass: "2", UpsizeEnabled: true})
	if err != nil || r.NodeName != "z-requested" || r.GrantedClass != "2" {
		t.Fatalf("reservation=%+v err=%v", r, err)
	}
	if _, err = kube.CoreV1().Pods("warm").Get(t.Context(), "warm", metav1.GetOptions{}); err != nil {
		t.Fatalf("upsized placeholder consumed: %v", err)
	}
}

func TestSharedPoolRefusesUnprovenWarmDeployment(t *testing.T) {
	for _, change := range []string{"missing", "wrong uid", "wrong deployment", "not controller", "not deployment"} {
		t.Run(change, func(t *testing.T) {
			b, kube := boundaryFixture()
			setSharedClass(t, b, "4")
			rs, err := kube.AppsV1().ReplicaSets("warm").Get(t.Context(), "warm", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "missing":
				err = kube.AppsV1().ReplicaSets("warm").Delete(t.Context(), rs.Name, metav1.DeleteOptions{})
			case "wrong uid":
				rs.UID = "replaced"
			case "wrong deployment":
				rs.OwnerReferences[0].Name = "controller"
			case "not controller":
				rs.OwnerReferences[0].Controller = ptr(false)
			case "not deployment":
				rs.OwnerReferences[0].Kind = "StatefulSet"
			}
			if err != nil {
				t.Fatal(err)
			}
			if change != "missing" {
				if _, err = kube.AppsV1().ReplicaSets("warm").Update(t.Context(), rs, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = kube.CoreV1().Pods("warm").Create(t.Context(), sharedWarmPod(), metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err = b.Reserve(t.Context(), boundaryBinding("team"), "job", CloudPlacement{RequestedClass: "2", UpsizeEnabled: true}); !errors.Is(err, ErrNoDedicatedNode) {
				t.Fatalf("reserve: %v", err)
			}
		})
	}
}

func TestSharedPoolForbiddenPlaceholderDeleteReleasesReservation(t *testing.T) {
	b, kube := boundaryFixture()
	setSharedClass(t, b, "4")
	if _, err := kube.CoreV1().Pods("warm").Create(t.Context(), sharedWarmPod(), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	kube.PrependReactor("delete", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, action.(ktesting.DeleteAction).GetName(), errors.New("RBAC denied"))
	})
	if _, err := b.Reserve(t.Context(), boundaryBinding("team"), "job", CloudPlacement{RequestedClass: "2", UpsizeEnabled: true}); !apierrors.IsForbidden(err) {
		t.Fatalf("reserve: %v", err)
	}
	node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if reservationEvidence(node) {
		t.Fatal("failed delete retained reservation")
	}
	if node.Labels[TeamNodeLabel] != TeamNodeValue("cloud", "team") || node.Labels[usedTeamLabel] != TeamNodeValue("cloud", "team") || node.Labels[usedRunLabel] != runNodeValue("cloud", "run") {
		t.Fatalf("permanent binding changed: %v", node.Labels)
	}
	if _, err = kube.CoreV1().Pods("warm").Get(t.Context(), "warm", metav1.GetOptions{}); err != nil {
		t.Fatalf("placeholder: %v", err)
	}
	other := boundaryBinding("other")
	if _, err = b.Reserve(t.Context(), other, "foreign", CloudPlacement{RequestedClass: "4"}); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatalf("foreign team: %v", err)
	}
	other = boundaryBinding("team")
	other.RunID = "other-run"
	if _, err = b.Reserve(t.Context(), other, "foreign", CloudPlacement{RequestedClass: "4"}); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatalf("foreign run: %v", err)
	}
}
