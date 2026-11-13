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
	if at, err := time.Parse(time.RFC3339Nano, job.Annotations[launchAnnotation]); err == nil {
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
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != "run-node" {
			continue
		}
		var at time.Time
		if status.State.Running != nil {
			at = status.State.Running.StartedAt.Time
		}
		if status.State.Terminated != nil {
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
	timing = timing.Retain(prior)
	if reflect.DeepEqual(timing, prior) {
		return
	}
	_, err = l.Control.Sync(ctx, []CloudJobObservation{{Binding: binding, Startup: &timing, Unheld: true, JobNamespace: job.Namespace, JobName: job.Name, JobUID: string(job.UID), NodeUID: r.NodeUID}})
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
