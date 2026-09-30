package launcher

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	// UnschedulableRelease is how long before the launcher hands the claim
	// of a Job that never got a machine back, well inside the claim's lease,
	// so the node waits in the queue unbilled with no attempt spent.
	UnschedulableRelease = 4 * time.Minute
	// SyncInterval is how often the launcher reconciles its Jobs with the
	// controller.
	SyncInterval = 5 * time.Second
	// MaxPendingJobs is how many of its Jobs may be not yet running before the
	// launcher stops claiming more, so a full pool does not pile up Pending
	// Jobs until each is handed back.
	MaxPendingJobs = 5
	// safety: under the controller's per-request cap, so any number of Jobs
	// syncs.
	syncBatch = 500
)

// Sync reconciles the launcher's Jobs with the controller: it deletes each Job
// whose claim ended or whose run is being cancelled, and hands back the claim
// of a Job that never got a machine.
func (l *Launcher) Sync(ctx context.Context) error {
	sel := metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=" + managedByLauncher}
	jobs, err := l.Kube.BatchV1().Jobs(l.Config.Namespace).List(ctx, sel)
	if err != nil {
		return fmt.Errorf("list jobs: %w", err)
	}
	pods, err := l.Kube.CoreV1().Pods(l.Config.Namespace).List(ctx, sel)
	if err != nil {
		return fmt.Errorf("list pods: %w", err)
	}
	unplaced := map[string]time.Duration{}
	started := map[string]bool{}
	for _, p := range pods.Items {
		if p.Status.Phase != corev1.PodPending {
			started[p.Labels[JobLabel]] = true
		}
		if since, ok := unschedulableSince(p); ok {
			unplaced[p.Labels[JobLabel]] = max(unplaced[p.Labels[JobLabel]], time.Since(since))
		}
	}
	claims := make([]store.LaunchJob, 0, len(jobs.Items))
	names := map[store.LaunchJob]string{}
	for _, j := range jobs.Items {
		gen, err := strconv.ParseInt(j.Annotations[GenerationAnnotation], 10, 64)
		// safety: a finished Job's pod holds its log until the Job's TTL
		// removes it, so only a Job still running is ever deleted here.
		if err != nil || j.Annotations[RunAnnotation] == "" || jobFinished(j) {
			continue
		}
		key := store.LaunchJob{RunID: j.Annotations[RunAnnotation], NodeID: j.Annotations[NodeAnnotation], Generation: gen}
		names[key] = j.Name
		key.Release = unplaced[j.Name] >= UnschedulableRelease
		claims = append(claims, key)
	}
	var results []store.LaunchJobResult
	for start := 0; start == 0 || start < len(claims); start += syncBatch {
		batch, err := l.Ctrl.LauncherSync(ctx, claims[start:min(start+syncBatch, len(claims))])
		if err != nil {
			return fmt.Errorf("sync jobs: %w", err)
		}
		results = append(results, batch...)
	}
	var errs []error
	pending := map[string]bool{}
	for _, name := range names {
		if !started[name] {
			pending[name] = true
		}
	}
	for _, r := range results {
		key := store.LaunchJob{RunID: r.RunID, NodeID: r.NodeID, Generation: r.Generation}
		name := names[key]
		if r.State != store.LaunchJobDelete || name == "" {
			continue
		}
		background := metav1.DeletePropagationBackground
		err := l.Kube.BatchV1().Jobs(l.Config.Namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &background})
		if err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, err)
			continue
		}
		delete(pending, name)
		l.Logger.Info("launcher: job deleted", "job", name, "run_id", key.RunID, "node_id", key.NodeID)
	}
	l.pending = pending
	return errors.Join(errs...)
}

func jobFinished(j batchv1.Job) bool {
	for _, c := range j.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func unschedulableSince(p corev1.Pod) (time.Time, bool) {
	if p.Status.Phase != corev1.PodPending {
		return time.Time{}, false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
			return c.LastTransitionTime.Time, true
		}
	}
	return time.Time{}, false
}
