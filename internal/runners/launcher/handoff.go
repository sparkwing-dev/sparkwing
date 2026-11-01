package launcher

import (
	"context"
	"errors"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (b *KubernetesBoundary) predecessorTeardown(ctx context.Context, node corev1.Node, successor CloudJobBinding) (bool, error) {
	held, binding, ok := nodeReservation(&node)
	if !ok || node.Annotations[reservationNamespaceAnnotation] != b.Namespace || binding.Team != successor.Team || binding.RunID != successor.RunID || sameBinding(binding, successor) || node.Labels[usedTeamLabel] != TeamNodeValue(b.Namespace, successor.Team) {
		return false, nil
	}
	if err := b.verifyNodeTeam(ctx, &node, successor.Team); err != nil {
		if errors.Is(err, ErrNoDedicatedNode) {
			return false, nil
		}
		return false, err
	}
	job, err := b.Kube.BatchV1().Jobs(b.Namespace).Get(ctx, held.JobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	recorded, reservation, err := jobOwnership(*job)
	matches := err == nil && reservation == held && sameBinding(recorded, binding)
	if !matches || job.DeletionTimestamp == nil || job.Annotations[rejectedAnnotation] != "" || !slices.Contains(job.Finalizers, cleanupFinalizer) {
		return false, nil
	}
	pods, err := b.Kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node.Name})
	if err != nil {
		return false, err
	}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName != node.Name || daemonPod(pod) {
			continue
		}
		if pod.Namespace != b.Namespace || !slices.ContainsFunc(pod.OwnerReferences, func(owner metav1.OwnerReference) bool {
			return owner.APIVersion == "batch/v1" && owner.Kind == "Job" && owner.Name == job.Name && owner.UID == job.UID && owner.Controller != nil && *owner.Controller
		}) {
			return false, nil
		}
	}
	return true, nil
}
