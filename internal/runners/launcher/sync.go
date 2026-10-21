package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ErrContainersRunning keeps the reservation until Kubernetes confirms every container has stopped.
var ErrContainersRunning = errors.New("Cloud Job containers are still running")

type bootstrapReference struct {
	Name   string    `json:"name"`
	UID    types.UID `json:"uid"`
	JobUID types.UID `json:"job_uid"`
}

// Sync reconstructs Kubernetes ownership and retires stale Jobs and reservations.
func (l *Launcher) Sync(ctx context.Context) error {
	if err := l.validate(); err != nil {
		return err
	}
	return l.sync(ctx)
}

func (l *Launcher) sync(ctx context.Context) error {
	var errs []error
	jobs, err := l.Kube.BatchV1().Jobs(l.Config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=" + managedByLauncher})
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("list Cloud Jobs: %w", err))...)
	}
	var observations []CloudJobObservation
	live := make(map[string]int)
	for i, job := range jobs.Items {
		binding, reservation, err := jobOwnership(job)
		if err != nil {
			errs = append(errs, l.discardMalformedJob(ctx, &job, err))
			continue
		}
		if job.Annotations[rejectedAnnotation] != "" {
			if err := l.rejectJob(ctx, &job, binding, reservation, job.Annotations[rejectedAnnotation]); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		// safety: a live suspended Job may still be publishing its bootstrap on another launcher.
		remove := job.DeletionTimestamp != nil || jobFinished(job)
		deadline, deadlineErr := time.Parse(time.RFC3339Nano, job.Annotations[deadlineAnnotation])
		if deadlineErr != nil {
			errs = append(errs, l.discardMalformedJob(ctx, &job, deadlineErr))
			continue
		}
		if !deadline.After(time.Now()) {
			remove = true
		}
		if !remove {
			if err := l.renewJobDeadline(ctx, &jobs.Items[i], binding, deadline); err != nil {
				errs = append(errs, err)
				continue
			}
			live[job.Name] = i
			observations = append(observations, CloudJobObservation{Binding: binding, JobNamespace: job.Namespace, JobName: job.Name, JobUID: string(job.UID), NodeUID: reservation.NodeUID})
		}
		if remove {
			if err := l.teardown(ctx, &job, reservation); err != nil && !errors.Is(err, ErrContainersRunning) {
				errs = append(errs, err)
			}
		}
	}
	if len(observations) > 0 {
		retirements, err := l.retirements(ctx, observations)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		for _, observation := range observations {
			if retirements[observation.JobName].Deferred {
				continue
			}
			job := &jobs.Items[live[observation.JobName]]
			_, reservation, err := jobOwnership(*job)
			if err == nil && !retirements[observation.JobName].Delete {
				if err := l.renewJobDeadline(ctx, job, observation.Binding, retirements[observation.JobName].LeaseExpiresAt); err != nil {
					errs = append(errs, err)
					continue
				}
				errs = append(errs, l.enforceAward(ctx, job, observation.Binding, reservation))
				continue
			}
			if err == nil {
				err = l.teardown(ctx, job, reservation)
			}
			if err != nil && !errors.Is(err, ErrContainersRunning) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(append(errs, l.syncReservations(ctx), l.syncCapacity(ctx))...)
}

func (l *Launcher) renewJobDeadline(ctx context.Context, job *batchv1.Job, binding CloudJobBinding, expires time.Time) error {
	expires, err := cloudLeaseDeadline(time.Now(), binding, expires)
	if err != nil {
		return err
	}
	start, err := time.Parse(time.RFC3339Nano, job.Annotations[launchAnnotation])
	if err != nil {
		return fmt.Errorf("Cloud Job has invalid launch time: %w", err)
	}
	if job.Status.StartTime != nil {
		start = job.Status.StartTime.Time
	}
	seconds := jobDeadlineSeconds(expires, start)
	if seconds <= 0 {
		return errors.New("Cloud lease deadline has elapsed")
	}
	if job.Spec.ActiveDeadlineSeconds != nil && *job.Spec.ActiveDeadlineSeconds == seconds && job.Annotations[deadlineAnnotation] == expires.Format(time.RFC3339Nano) {
		return nil
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"uid": job.UID, "resourceVersion": job.ResourceVersion, "annotations": map[string]string{deadlineAnnotation: expires.Format(time.RFC3339Nano)}}, "spec": map[string]any{"activeDeadlineSeconds": seconds}})
	if err != nil {
		return err
	}
	updated, err := l.Kube.BatchV1().Jobs(l.Config.Namespace).Patch(ctx, job.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return err
	}
	expected := job.DeepCopy()
	expected.Spec.ActiveDeadlineSeconds = &seconds
	expected.Annotations[deadlineAnnotation] = expires.Format(time.RFC3339Nano)
	if err := validateAdmittedJob(expected, updated); err != nil {
		return err
	}
	*job = *updated
	return nil
}

func (l *Launcher) syncReservations(ctx context.Context) error {
	nodes, err := l.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	var errs []error
	budget := map[string]int{}
	blocked := map[string]bool{}
	// safety: the protected node label fixes the budget to a team even when
	// a kubelet has damaged the reservation annotations.
	quarantine := func(node *corev1.Node, cause string) error {
		label := node.Labels[TeamNodeLabel]
		if budget[label] >= maxQuarantinesPerSync && label != "" {
			blocked[label] = true
			return &nodeFault{node: node.Name, cause: fmt.Sprintf("%s; more than %d nodes faulted for this team, so its claims stop until an operator checks these nodes", cause, maxQuarantinesPerSync)}
		}
		budget[label]++
		return quarantineNode(ctx, l.Kube, node, cause)
	}
	for i := range nodes.Items {
		node := &nodes.Items[i]
		if namespace := node.Annotations[reservationNamespaceAnnotation]; namespace != l.Config.Namespace {
			if namespace != "" {
				l.logger().Warn("node holds a Cloud reservation from another job namespace; drain it with that namespace's launcher or clear it by hand", "node", node.Name, "namespace", namespace)
			}
			continue
		}
		held, binding, ok := nodeReservation(node)
		if !ok {
			errs = append(errs, quarantine(node, "reservation is malformed or does not match its award"))
			continue
		}
		refused, err := l.releaseOrphan(ctx, held, binding)
		if refused {
			err = quarantine(node, "controller refused the reservation's award")
		}
		errs = append(errs, err)
	}
	l.mu.Lock()
	l.blockedTeamLabels = l.blockedTeamLabels[:0]
	for label := range blocked {
		l.blockedTeamLabels = append(l.blockedTeamLabels, label)
	}
	slices.Sort(l.blockedTeamLabels)
	l.mu.Unlock()
	return errors.Join(errs...)
}

func nodeReservation(node *corev1.Node) (CloudNodeReservation, CloudJobBinding, bool) {
	var held CloudNodeReservation
	var binding CloudJobBinding
	if json.Unmarshal([]byte(node.Annotations[reservationAnnotation]), &held) != nil || json.Unmarshal([]byte(node.Annotations[bindingAnnotation]), &binding) != nil {
		return held, binding, false
	}
	lease := binding.LeaseRef()
	return held, binding, held.NodeName == node.Name && held.NodeUID == string(node.UID) && held.Team != "" && held.Team == binding.Team && node.Labels[TeamNodeLabel] == TeamNodeValue(node.Annotations[reservationNamespaceAnnotation], held.Team) &&
		held.Award == lease && lease.Kind != "" && lease.ID != "" && lease.Holder != "" && lease.Generation > 0 && held.JobName == jobName(binding) && held.ReservationID != ""
}

// maxQuarantinesPerSync bounds faulted nodes per team in each pass.
const maxQuarantinesPerSync = 3

// safety: each orphaned reservation gets its own controller call, so a refused
// award names only the node that recorded it.
func (l *Launcher) releaseOrphan(ctx context.Context, held CloudNodeReservation, binding CloudJobBinding) (refused bool, err error) {
	jobs := l.Kube.BatchV1().Jobs(l.Config.Namespace)
	_, err = jobs.Get(ctx, held.JobName, metav1.GetOptions{})
	if err == nil || !apierrors.IsNotFound(err) {
		return false, err
	}
	states, err := l.retirements(ctx, []CloudJobObservation{{Binding: binding, JobName: held.JobName, NodeUID: held.NodeUID, ReservationID: held.ReservationID}})
	if reservationRefused(err) {
		return true, nil
	}
	if err != nil || !states[held.JobName].Delete {
		return false, err
	}
	if _, err = jobs.Get(ctx, held.JobName, metav1.GetOptions{}); err == nil {
		return false, nil
	}
	if apierrors.IsNotFound(err) {
		err = l.Boundary.Release(ctx, held)
	}
	if errors.Is(err, ErrContainersRunning) {
		return false, nil
	}
	return false, err
}

func (l *Launcher) retirements(ctx context.Context, observations []CloudJobObservation) (map[string]CloudJobState, error) {
	states, err := l.Control.Sync(ctx, observations)
	if err != nil {
		return nil, err
	}
	if len(states) != len(observations) {
		return nil, errors.New("controller sync omitted or added Cloud Job decisions")
	}
	bindings := make(map[string]CloudJobBinding, len(observations))
	for _, observation := range observations {
		if _, exists := bindings[observation.JobName]; exists {
			return nil, errors.New("duplicate Cloud Job observation")
		}
		bindings[observation.JobName] = observation.Binding
	}
	decisions := make(map[string]CloudJobState, len(states))
	for _, state := range states {
		name := jobName(state.Binding)
		binding, found := bindings[name]
		_, duplicate := decisions[name]
		if !found || duplicate || !sameBinding(state.Binding, binding) {
			return nil, errAwardMismatch
		}
		decisions[name] = state
	}
	return decisions, nil
}

func sameBinding(a, b CloudJobBinding) bool {
	// safety: transport preserves instants, not Go's monotonic clock or time-zone identity.
	if !a.OwnerDeadline.Equal(b.OwnerDeadline) || !a.AttemptDeadline.Equal(b.AttemptDeadline) || !a.ParentCapabilityExpiry.Equal(b.ParentCapabilityExpiry) {
		return false
	}
	a.OwnerDeadline, a.AttemptDeadline, a.ParentCapabilityExpiry = b.OwnerDeadline, b.AttemptDeadline, b.ParentCapabilityExpiry
	return a == b
}

func jobOwnership(job batchv1.Job) (CloudJobBinding, CloudNodeReservation, error) {
	var binding CloudJobBinding
	var reservation CloudNodeReservation
	if err := json.Unmarshal([]byte(job.Annotations[bindingAnnotation]), &binding); err != nil {
		return binding, reservation, fmt.Errorf("Job %s has invalid binding: %w", job.Name, err)
	}
	if err := json.Unmarshal([]byte(job.Annotations[reservationAnnotation]), &reservation); err != nil {
		return binding, reservation, fmt.Errorf("Job %s has invalid reservation: %w", job.Name, err)
	}
	if raw := job.Annotations[bootstrapAnnotation]; raw != "" {
		var ref bootstrapReference
		if err := json.Unmarshal([]byte(raw), &ref); err != nil || ref.Name == "" || ref.UID == "" || ref.JobUID != job.UID {
			return binding, reservation, errors.New("Cloud Job has invalid bootstrap ownership")
		}
	}
	if job.Name != jobName(binding) || reservation.JobName != job.Name || reservation.Team != binding.Team || reservation.Award != binding.LeaseRef() || reservation.NodeName != job.Spec.Template.Spec.NodeName || reservation.NodeUID == "" || reservation.ReservationID == "" {
		return binding, reservation, errors.New("Cloud Job ownership does not match its reservation")
	}
	return binding, reservation, nil
}

func (l *Launcher) teardown(ctx context.Context, job *batchv1.Job, reservation CloudNodeReservation) error {
	namespace := l.Config.Namespace
	bootstrap := bootstrapReference{}
	if reference := job.Annotations[bootstrapAnnotation]; reference != "" {
		bootstrap = bootstrapReference{}
		if err := json.Unmarshal([]byte(reference), &bootstrap); err != nil || bootstrap.Name == "" || bootstrap.UID == "" || bootstrap.JobUID == "" || bootstrap.JobUID != job.UID {
			return l.discardMalformedJob(ctx, job, fmt.Errorf("Job %s has invalid bootstrap cleanup ownership", job.Name))
		}
	}
	foreground := metav1.DeletePropagationForeground
	jobErr := l.Kube.BatchV1().Jobs(namespace).Delete(ctx, job.Name, metav1.DeleteOptions{PropagationPolicy: &foreground, Preconditions: &metav1.Preconditions{UID: &job.UID}})
	if apierrors.IsNotFound(jobErr) {
		jobErr = nil
	}
	if jobErr != nil {
		// safety: failed UID fencing cannot authorize deletion of a replacement Job's bootstrap.
		current, lookupErr := l.Kube.BatchV1().Jobs(namespace).Get(ctx, job.Name, metav1.GetOptions{})
		if lookupErr != nil || current.UID != job.UID {
			return errors.Join(jobErr, lookupErr)
		}
	}
	secretErr := l.deleteBootstrap(ctx, bootstrap)
	if err := errors.Join(jobErr, secretErr); err != nil {
		return err
	}
	pods, err := l.Kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: JobLabel + "=" + job.Name})
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		if !podStopped(pod) {
			return ErrContainersRunning
		}
	}
	if err = l.Boundary.Release(ctx, reservation); err != nil {
		return err
	}
	if !slices.Contains(job.Finalizers, cleanupFinalizer) {
		return nil
	}
	finalizers := slices.DeleteFunc(slices.Clone(job.Finalizers), func(s string) bool { return s == cleanupFinalizer })
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"uid": job.UID, "resourceVersion": job.ResourceVersion, "finalizers": finalizers}})
	if err != nil {
		return err
	}
	_, err = l.Kube.BatchV1().Jobs(namespace).Patch(ctx, job.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (l *Launcher) deleteBootstrap(ctx context.Context, bootstrap bootstrapReference) error {
	if bootstrap.Name == "" {
		return nil
	}
	if bootstrap.UID == "" {
		return errors.New("bootstrap cleanup requires an owned Secret UID")
	}
	options := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &bootstrap.UID}}
	err := l.Kube.CoreV1().Secrets(l.Config.Namespace).Delete(ctx, bootstrap.Name, options)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func podStopped(pod corev1.Pod) bool {
	// safety: an absent status on a live pod is not evidence that its Docker sidecar stopped.
	if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
		return false
	}
	statuses := append(slices.Clone(pod.Status.InitContainerStatuses), pod.Status.ContainerStatuses...)
	statuses = append(statuses, pod.Status.EphemeralContainerStatuses...)
	for _, status := range statuses {
		if status.State.Running != nil {
			return false
		}
	}
	return true
}

func jobFinished(job batchv1.Job) bool {
	for _, condition := range job.Status.Conditions {
		if (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed) && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
