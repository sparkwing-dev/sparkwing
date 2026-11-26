package launcher

import (
	"context"
	"errors"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
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
	matches := err == nil && sameReservationFence(reservation, held) && sameBinding(recorded, binding)
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

// perf: a Pod that outlasts this wait costs no more than the reconcile pass it would otherwise wait for.
const handoffWait = time.Second

func (l *Launcher) retireEndedPredecessors(ctx context.Context, successor CloudJobBinding, planOnly bool) ([]string, error) {
	selector := "app.kubernetes.io/managed-by=" + managedByLauncher + "," + TeamLabel + "=" + TeamNodeValue(l.Config.Namespace, successor.Team)
	jobs, err := l.Kube.BatchV1().Jobs(l.Config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	var ending []string
	var observations []CloudJobObservation
	live := map[string]int{}
	reservations := map[string]CloudNodeReservation{}
	for i, job := range jobs.Items {
		binding, reservation, err := jobOwnership(job)
		if err != nil || job.Annotations[rejectedAnnotation] != "" || binding.Team != successor.Team || binding.RunID != successor.RunID || sameBinding(binding, successor) {
			continue
		}
		if job.DeletionTimestamp != nil {
			ending = append(ending, job.Name)
			continue
		}
		// perf: siblings in a fan-out are live Jobs too, so asking only about the plan adds no controller call per claim.
		if planOnly && binding.Kind != CloudPlan {
			continue
		}
		live[job.Name], reservations[job.Name] = i, reservation
		observations = append(observations, CloudJobObservation{Binding: binding, JobNamespace: job.Namespace, JobName: job.Name, JobUID: string(job.UID), NodeUID: reservation.NodeUID})
	}
	if len(observations) == 0 {
		return ending, nil
	}
	states, err := l.retirements(ctx, observations)
	if err != nil {
		return ending, err
	}
	var errs []error
	for _, observation := range observations {
		if state := states[observation.JobName]; !state.Delete || state.Deferred {
			continue
		}
		job := &jobs.Items[live[observation.JobName]]
		errs = append(errs, teardownFault(job.Name, l.teardown(ctx, job, reservations[job.Name])))
		ending = append(ending, job.Name)
	}
	return ending, errors.Join(errs...)
}

func (l *Launcher) handoffEvent(event watch.Event) (until time.Time, gone string, awaited bool) {
	pod, ok := event.Object.(*corev1.Pod)
	if !ok || pod.Labels[JobLabel] == "" {
		return time.Time{}, "", false
	}
	job := pod.Labels[JobLabel]
	now := time.Now()
	for _, award := range l.waitingAwards() {
		if now.Before(award.handoffUntil) && slices.Contains(award.handoffJobs, job) {
			if event.Type == watch.Deleted {
				gone = job
			}
			return award.handoffUntil, gone, true
		}
	}
	return time.Time{}, "", false
}

// safety: Release refuses while any Pod remains on the node, so a successor placed here never shares it.
func (l *Launcher) finishHandoffs(ctx context.Context, gone []string) error {
	var errs []error
	now := time.Now()
	for _, award := range l.waitingAwards() {
		matched := slices.DeleteFunc(slices.Clone(award.handoffJobs), func(job string) bool { return !slices.Contains(gone, job) })
		if !now.Before(award.handoffUntil) || len(matched) == 0 {
			continue
		}
		released, err := l.releasePredecessors(ctx, matched)
		if err != nil || !released {
			errs = append(errs, err)
			continue
		}
		award.handoffUntil, award.handoffJobs = time.Time{}, nil
		errs = append(errs, l.place(ctx, award))
	}
	return errors.Join(errs...)
}

// safety: the controller already ended these Jobs, and a Job is gone only after Release freed its node.
func (l *Launcher) releasePredecessors(ctx context.Context, names []string) (bool, error) {
	released := false
	for _, name := range names {
		job, err := l.Kube.BatchV1().Jobs(l.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			released = true
			continue
		}
		if err != nil {
			return false, err
		}
		_, reservation, err := jobOwnership(*job)
		if err != nil || job.DeletionTimestamp == nil {
			continue
		}
		err = l.teardown(ctx, job, reservation)
		if errors.Is(err, errContainersRunning) {
			continue
		}
		if err != nil {
			return false, teardownFault(name, err)
		}
		released = true
	}
	return released, nil
}
