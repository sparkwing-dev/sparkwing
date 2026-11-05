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
	// perf: work can be awarded before Kubernetes observes the planning Job's exit.
	planHandoff := binding.Kind == CloudPlan && successor.Kind != CloudPlan
	if !matches || job.Annotations[rejectedAnnotation] != "" || !slices.Contains(job.Finalizers, cleanupFinalizer) {
		return false, nil
	}
	finishing := job.DeletionTimestamp != nil || jobFinished(*job) || planHandoff
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
		// perf: Pod exit can precede Job deletion while the controller seals the logs.
		finishing = finishing || pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed || slices.ContainsFunc(pod.Status.ContainerStatuses, func(status corev1.ContainerStatus) bool {
			return status.Name == "run-node" && status.State.Terminated != nil
		})
	}
	return finishing, nil
}

func (l *Launcher) retireEndedPredecessors(ctx context.Context, successor CloudJobBinding) (bool, error) {
	jobs, err := l.Kube.BatchV1().Jobs(l.Config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=" + managedByLauncher})
	if err != nil {
		return false, err
	}
	var observations []CloudJobObservation
	live := map[string]int{}
	reservations := map[string]CloudNodeReservation{}
	for i, job := range jobs.Items {
		binding, reservation, err := jobOwnership(job)
		if err != nil || job.DeletionTimestamp != nil || job.Annotations[rejectedAnnotation] != "" || binding.Team != successor.Team || binding.RunID != successor.RunID || sameBinding(binding, successor) {
			continue
		}
		live[job.Name], reservations[job.Name] = i, reservation
		observations = append(observations, CloudJobObservation{Binding: binding, JobNamespace: job.Namespace, JobName: job.Name, JobUID: string(job.UID), NodeUID: reservation.NodeUID})
	}
	if len(observations) == 0 {
		return false, nil
	}
	states, err := l.retirements(ctx, observations)
	if err != nil {
		return false, err
	}
	retired := false
	var errs []error
	for _, observation := range observations {
		if state := states[observation.JobName]; !state.Delete || state.Deferred {
			continue
		}
		job := &jobs.Items[live[observation.JobName]]
		errs = append(errs, teardownFault(job.Name, l.teardown(ctx, job, reservations[job.Name])))
		retired = true
	}
	return retired, errors.Join(errs...)
}
