package launcher

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func sharedPool(namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.sh/v1", "kind": "NodePool", "metadata": map[string]any{"name": "shared-pool"},
		"spec": map[string]any{"template": map[string]any{
			"metadata": map[string]any{"labels": map[string]any{CloudClassLabel: "4", PoolNamespaceLabel: namespace}},
			"spec":     map[string]any{"nodeClassRef": map[string]any{"group": "karpenter.k8s.aws", "kind": "EC2NodeClass", "name": "sparkwing-jobs"}, "taints": []any{map[string]any{"key": DedicatedTaint, "value": "true", "effect": "NoSchedule"}}},
		}},
	}}
}

func poolClient(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{nodePools: "NodePoolList"}, objects...)
}

func TestPoolConfigurationIgnoresScalingAndExhaustion(t *testing.T) {
	value := "cloud"
	for _, test := range []struct {
		name       string
		pool       *unstructured.Unstructured
		configured bool
	}{
		{"missing", nil, false},
		{"another team", sharedPool("other"), false},
		{"another deployment", sharedPool("staging"), false},
		{"no nodes", sharedPool(value), true},
		{"wrong node class", func() *unstructured.Unstructured {
			p := sharedPool(value)
			_ = unstructured.SetNestedField(p.Object, "unhardened", "spec", "template", "spec", "nodeClassRef", "name")
			return p
		}(), false},
		{"wrong CPU class", func() *unstructured.Unstructured {
			p := sharedPool(value)
			_ = unstructured.SetNestedSlice(p.Object, []any{map[string]any{"key": "karpenter.k8s.aws/instance-cpu", "operator": "In", "values": []any{"2"}}}, "spec", "template", "spec", "requirements")
			return p
		}(), false},
		{"larger CPU class", func() *unstructured.Unstructured {
			p := sharedPool(value)
			_ = unstructured.SetNestedSlice(p.Object, []any{map[string]any{"key": "karpenter.k8s.aws/instance-cpu", "operator": "In", "values": []any{"2", "8"}}}, "spec", "template", "spec", "requirements")
			return p
		}(), true},
		{"wrong memory class", func() *unstructured.Unstructured {
			p := sharedPool(value)
			_ = unstructured.SetNestedSlice(p.Object, []any{map[string]any{"key": "karpenter.k8s.aws/instance-memory", "operator": "Lt", "values": []any{"8192"}}}, "spec", "template", "spec", "requirements")
			return p
		}(), false},
		{"not ready and exhausted", func() *unstructured.Unstructured {
			p := sharedPool(value)
			p.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False"}}, "resources": map[string]any{"cpu": "8"}}
			p.Object["spec"].(map[string]any)["limits"] = map[string]any{"cpu": "8"}
			return p
		}(), true},
		{"no protected taint", func() *unstructured.Unstructured {
			p := sharedPool(value)
			unstructured.RemoveNestedField(p.Object, "spec", "template", "spec", "taints")
			return p
		}(), false},
		{"other hardware OS", func() *unstructured.Unstructured {
			p := sharedPool(value)
			_ = unstructured.SetNestedSlice(p.Object, []any{map[string]any{"key": corev1.LabelOSStable, "operator": "In", "values": []any{"windows"}}}, "spec", "template", "spec", "requirements")
			return p
		}(), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var objects []runtime.Object
			if test.pool != nil {
				objects = append(objects, test.pool)
			}
			b := &KubernetesBoundary{Namespace: "cloud", NodeClassName: "sparkwing-jobs", Karpenter: poolClient(objects...)}
			got, err := b.CapacityConfigured(t.Context(), corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")})
			if err != nil || got != test.configured {
				t.Fatalf("configured=%t err=%v", got, err)
			}
		})
	}
}

type capacityBoundary struct {
	CloudBoundary
	pools *KubernetesBoundary
}

func (b capacityBoundary) CapacityConfigured(ctx context.Context, budget corev1.ResourceList) (bool, error) {
	return b.pools.CapacityConfigured(ctx, budget)
}

func TestUnconfiguredTeamDeletesDemandAndReportsOnce(t *testing.T) {
	l, kube, boundary, control := fixture()
	boundary.reserveErr = ErrNoDedicatedNode
	l.Boundary = capacityBoundary{CloudBoundary: boundary, pools: &KubernetesBoundary{Namespace: "cloud", NodeClassName: "sparkwing-jobs", Karpenter: poolClient()}}
	var logs bytes.Buffer
	l.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	if err := l.requestCapacity(t.Context(), *control.award); err != nil {
		t.Fatal(err)
	}
	var failures int
	l.Control = publicationControl{CloudControl: control, sync: func(observations []CloudJobObservation) ([]CloudJobState, error) {
		states := make([]CloudJobState, len(observations))
		for i, observation := range observations {
			if observation.Capacity == nil || !observation.Capacity.Unconfigured {
				t.Fatal("missing configuration diagnosis")
			}
			failures++
			states[i] = CloudJobState{Binding: observation.Binding, Delete: true}
		}
		return states, nil
	}}
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	control.award = nil
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs, err := kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 0 || len(capacityPods(t, kube)) != 0 || len(l.waitingAwards()) != 0 || failures != 1 {
		t.Fatalf("terminal capacity cleanup: failures=%d jobs=%d err=%v", failures, len(jobs.Items), err)
	}
	if strings.Count(logs.String(), "requested_class=") != 1 || !strings.Contains(logs.String(), "namespace=cloud") {
		t.Fatalf("operator guidance: %s", logs.String())
	}
}

func TestPoolLookupFailureWaitsThenPoolAndNodeAppear(t *testing.T) {
	l, kube, boundary, control := fixture()
	control.award.CPUCores = 4
	boundary.reserveErr = ErrNoDedicatedNode
	pools := poolClient()
	l.Boundary = capacityBoundary{CloudBoundary: boundary, pools: &KubernetesBoundary{Namespace: "cloud", NodeClassName: "sparkwing-jobs", Karpenter: pools}}
	uncertain := true
	pools.PrependReactor("list", "nodepools", func(ktesting.Action) (bool, runtime.Object, error) {
		if uncertain {
			return true, nil, errors.New("pool API unavailable")
		}
		return false, nil, nil
	})
	if _, err := l.LaunchOne(t.Context()); err == nil {
		t.Fatal("pool API failure ignored")
	}
	if len(l.waitingAwards()) != 1 {
		t.Fatal("uncertain diagnosis discarded award")
	}
	control.award = nil
	if _, err := pools.Resource(nodePools).Create(t.Context(), sharedPool("cloud"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	uncertain = false
	if _, err := l.LaunchOne(t.Context()); err != nil || len(capacityPods(t, kube)) != 1 {
		t.Fatalf("pool did not wait for scaling: %v", err)
	}
	boundary.reserveErr = nil
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs, err := kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 || len(capacityPods(t, kube)) != 0 {
		t.Fatalf("scaled capacity did not run: jobs=%d err=%v", len(jobs.Items), err)
	}
}

func TestOverCeilingAwardRetriesCapacityFailure(t *testing.T) {
	l, _, _, control := fixture()
	l.Config.CPUCeiling = 1
	unavailable := true
	l.Control = publicationControl{CloudControl: control, sync: func(observations []CloudJobObservation) ([]CloudJobState, error) {
		if unavailable {
			return nil, errors.New("controller unavailable")
		}
		if len(observations) != 1 || observations[0].Capacity == nil || !observations[0].Capacity.Unconfigured {
			t.Fatal("missing configuration diagnosis")
		}
		return []CloudJobState{{Binding: observations[0].Binding, Delete: true}}, nil
	}}
	if _, err := l.LaunchOne(t.Context()); err == nil || len(l.waitingAwards()) != 1 {
		t.Fatalf("failure lost its award before acknowledgement: %v", err)
	}
	unavailable, control.award = false, nil
	if _, err := l.LaunchOne(t.Context()); err != nil || len(l.waitingAwards()) != 0 {
		t.Fatalf("capacity failure did not retry: %v", err)
	}
}
