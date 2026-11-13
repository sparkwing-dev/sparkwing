package launcher

import (
	"context"
	"reflect"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func (l *Launcher) observeStartup(ctx context.Context, job *batchv1.Job, binding CloudJobBinding, r CloudNodeReservation, pod *corev1.Pod) {
	l.mu.Lock()
	prior := l.startupObserved[pod.UID]
	l.mu.Unlock()
	if prior.ImagePulledAt != nil && prior.ContainerStartedAt != nil {
		return
	}
	timing := capability.PodStartup{PodUID: string(pod.UID)}
	if !job.CreationTimestamp.IsZero() {
		at := job.CreationTimestamp.Time.UTC()
		timing.JobCreatedAt = &at
	}
	if job.Status.StartTime != nil && !job.Status.StartTime.IsZero() {
		at := job.Status.StartTime.Time.UTC()
		timing.JobActivatedAt = &at
	}
	if !pod.CreationTimestamp.IsZero() {
		at := pod.CreationTimestamp.Time.UTC()
		timing.PodCreatedAt = &at
	}

	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionTrue && !condition.LastTransitionTime.IsZero() {
			at := condition.LastTransitionTime.Time.UTC()
			timing.ScheduledAt = &at
		}
	}
	started := false
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != "run-node" {
			continue
		}
		var at time.Time
		if status.State.Running != nil {
			started = true
			at = status.State.Running.StartedAt.Time
		}
		if status.State.Terminated != nil {
			started = true
			at = status.State.Terminated.StartedAt.Time
		}
		if !at.IsZero() {
			at = at.UTC()
			timing.ContainerStartedAt = &at
		}
	}
	if timing.PodCreatedAt == nil && timing.ScheduledAt == nil && timing.ContainerStartedAt == nil {
		return
	}
	if l.probeStartupImage(pod.UID, started) {
		events, err := l.Kube.CoreV1().Events(pod.Namespace).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.uid=" + string(pod.UID)})
		if err != nil {
			// safety: unavailable diagnostic events must not change execution or placement.
			l.logger().WarnContext(ctx, "Cloud startup image timing unavailable")
		} else {
			for _, event := range events.Items {
				if event.InvolvedObject.UID != pod.UID || event.InvolvedObject.Kind != "Pod" || !strings.HasSuffix(event.InvolvedObject.FieldPath, "{run-node}") {
					continue
				}
				at := event.FirstTimestamp.Time
				if at.IsZero() {
					at = event.EventTime.Time
				}
				if at.IsZero() {
					at = event.LastTimestamp.Time
				}
				if at.IsZero() {
					continue
				}
				at = at.UTC()
				switch event.Reason {
				case "Pulling":
					if timing.ImagePullStartedAt == nil || at.Before(*timing.ImagePullStartedAt) {
						timing.ImagePullStartedAt = &at
					}
				case "Pulled":
					if timing.ImagePulledAt == nil || at.Before(*timing.ImagePulledAt) {
						timing.ImagePulledAt = &at
					}
				}
			}
		}
	}
	timing = timing.Retain(prior)
	if reflect.DeepEqual(timing, prior) {
		return
	}
	_, err := l.Control.Sync(ctx, []CloudJobObservation{{Binding: binding, Startup: &timing, Unheld: true, JobNamespace: job.Namespace, JobName: job.Name, JobUID: string(job.UID), NodeUID: r.NodeUID}})
	if err != nil {
		l.logger().WarnContext(ctx, "Cloud startup timing could not be recorded")
		return
	}
	l.mu.Lock()
	if l.startupObserved == nil {
		l.startupObserved = map[types.UID]capability.PodStartup{}
	}
	l.startupObserved[pod.UID] = timing
	l.mu.Unlock()
}

// imageProbeBudget counts Events LISTs before and after the run-node container starts.
type imageProbeBudget struct{ beforeStart, afterStart int }

// safety: missing or unreadable Events must not add API traffic for a Pod's
// whole lifetime, whether it never starts (unschedulable, image backoff) or runs
// for hours, even when publishing its timing receipt also fails. Image Events
// outlive the pull, so post-start probes still find what a spent pre-start
// budget missed.
func (l *Launcher) probeStartupImage(pod types.UID, started bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	budget := l.startupImageProbes[pod]
	count := &budget.beforeStart
	if started {
		count = &budget.afterStart
	}
	if *count >= 3 {
		return false
	}
	*count++
	if l.startupImageProbes == nil {
		l.startupImageProbes = map[types.UID]imageProbeBudget{}
	}
	l.startupImageProbes[pod] = budget
	return true
}
