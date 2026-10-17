package acceptance_test

import (
	"fmt"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func cloudKubeW8(objects ...runtime.Object) *fake.Clientset {
	objects = append(objects, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cloud", Labels: map[string]string{"kubernetes.io/metadata.name": "cloud"}}})
	kube := fake.NewClientset(objects...)
	jobs := 0
	kube.PrependReactor("create", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		creation := action.(ktesting.CreateAction)
		switch object := creation.GetObject().(type) {
		case *batchv1.Job:
			jobs++
			if object.UID == "" {
				object.UID = "job-uid"
				if jobs > 1 {
					object.UID = types.UID(fmt.Sprintf("job-uid-%d", jobs))
				}
			}
		case *corev1.Secret:
			if object.UID == "" {
				object.UID = types.UID("secret-uid-" + object.Name)
			}
		}
		return false, nil, nil
	})
	kube.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		namespace := action.GetNamespace()
		pods, err := kube.Tracker().List(corev1.SchemeGroupVersion.WithResource("pods"), corev1.SchemeGroupVersion.WithKind("Pod"), namespace)
		if err != nil {
			return true, nil, err
		}
		jobs, err := kube.Tracker().List(batchv1.SchemeGroupVersion.WithResource("jobs"), batchv1.SchemeGroupVersion.WithKind("Job"), namespace)
		if err != nil {
			return true, nil, err
		}
		list := pods.(*corev1.PodList)
		for _, job := range jobs.(*batchv1.JobList).Items {
			if job.DeletionTimestamp != nil || (job.Spec.Suspend != nil && *job.Spec.Suspend) {
				continue
			}
			found := false
			for _, pod := range list.Items {
				if pod.Labels[launcher.JobLabel] == job.Name || pod.GenerateName == job.Name+"-" {
					found = true
				}
			}
			if !found {
				pod := corev1.Pod{ObjectMeta: *job.Spec.Template.ObjectMeta.DeepCopy(), Spec: *job.Spec.Template.Spec.DeepCopy()}
				pod.Name, pod.Namespace, pod.UID = job.Name+"-pod", job.Namespace, "pod-uid"
				pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: cloudPtrW8(true)}}
				list.Items = append(list.Items, pod)
			}
		}
		return true, list, nil
	})
	return kube
}

func cloudPtrW8[T any](v T) *T { return &v }

func cloudProvisionerW8(node *corev1.Node, team string) dynamic.Interface {
	node.OwnerReferences = []metav1.OwnerReference{{APIVersion: "karpenter.sh/v1", Kind: "NodeClaim", Name: "claim", UID: "claim-uid"}}
	node.Spec.ProviderID = "aws:///zone/i-node"
	claim := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.sh/v1", "kind": "NodeClaim",
		"metadata": map[string]any{"name": "claim", "uid": "claim-uid", "labels": map[string]any{launcher.TeamNodeLabel: launcher.TeamNodeValue(team), "karpenter.sh/nodepool": "pool"}, "ownerReferences": []any{map[string]any{"apiVersion": "karpenter.sh/v1", "kind": "NodePool", "name": "pool", "uid": "pool-uid"}}},
		"status":   map[string]any{"nodeName": node.Name, "providerID": node.Spec.ProviderID},
	}}
	pool := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.sh/v1", "kind": "NodePool", "metadata": map[string]any{"name": "pool", "uid": "pool-uid"},
		"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{launcher.TeamNodeLabel: launcher.TeamNodeValue(team)}}}},
	}}
	return dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), claim, pool)
}
