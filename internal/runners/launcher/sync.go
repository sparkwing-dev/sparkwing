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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	// UnschedulableWait is how long a Job's pod may wait for a machine before
	// the launcher stops claiming: past a cold start, a pod still unplaced
	// means the pool is full or the fleet is closed to new machines.
	UnschedulableWait = 2 * time.Minute
	// UnschedulableRelease is how long before the launcher hands the claim
	// of a Job that never got a machine back, well inside the claim's lease,
	// so the node waits in the queue unbilled with no attempt spent.
	UnschedulableRelease = 4 * time.Minute
	// SyncInterval is how often the launcher reconciles its Jobs with the
	// controller.
	SyncInterval = 5 * time.Second
)

// Sync reconciles the launcher's Jobs with the controller: it deletes each Job
// whose claim ended or whose run is being cancelled, hands back the claim of a
// Job that never got a machine, and reports why it is not claiming, if it is
// not. It returns that reason, empty when there is capacity to claim.
func (l *Launcher) Sync(ctx context.Context) (string, error) {
	sel := metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=" + managedByLauncher}
	jobs, err := l.Kube.BatchV1().Jobs(l.Config.Namespace).List(ctx, sel)
	if err != nil {
		return "", fmt.Errorf("list jobs: %w", err)
	}
	pods, err := l.Kube.CoreV1().Pods(l.Config.Namespace).List(ctx, sel)
	if err != nil {
		return "", fmt.Errorf("list pods: %w", err)
	}
	unplaced := map[string]time.Duration{}
	for _, p := range pods.Items {
		if since, ok := unschedulableSince(p); ok {
			unplaced[p.Labels[JobLabel]] = max(unplaced[p.Labels[JobLabel]], time.Since(since))
		}
	}
	var reason string
	claims := make([]store.LaunchJob, 0, len(jobs.Items))
	names := map[store.LaunchJob]string{}
	for _, j := range jobs.Items {
		gen, err := strconv.ParseInt(j.Annotations[GenerationAnnotation], 10, 64)
		// safety: a finished Job's pod holds its log until the Job's TTL
		// removes it, so only a Job still running is ever deleted here.
		if err != nil || j.Annotations[RunAnnotation] == "" || jobFinished(j) {
			continue
		}
		waited := unplaced[j.Name]
		if waited >= UnschedulableWait {
			reason = "a Job has waited over " + UnschedulableWait.String() + " for a machine"
		}
		key := store.LaunchJob{RunID: j.Annotations[RunAnnotation], NodeID: j.Annotations[NodeAnnotation], Generation: gen}
		names[key] = j.Name
		key.Release = waited >= UnschedulableRelease
		claims = append(claims, key)
	}
	if reason == "" && l.Capacity != nil {
		if reason, err = l.Capacity(ctx); err != nil {
			l.Logger.Warn("launcher: capacity check failed; claiming anyway", "err", err)
		}
	}
	results, err := l.Ctrl.LauncherSync(ctx, claims, reason)
	if err != nil {
		return reason, fmt.Errorf("sync jobs: %w", err)
	}
	var errs []error
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
		l.Logger.Info("launcher: job deleted", "job", name, "run_id", key.RunID, "node_id", key.NodeID)
	}
	return reason, errors.Join(errs...)
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

var nodePoolResource = schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}

// safety: the Job pool's smallest machine has 4 cores, so less CPU left under
// its limit starts no machine at all.
const minNodeCores = 4

// NodePoolCapacity reports why the Karpenter NodePool named pool can start no
// machine for another Job, or "" when it can: its CPU limit, less the CPU of
// the machines it already runs, is under the smallest machine it starts.
func NodePoolCapacity(dyn dynamic.Interface, pool string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		obj, err := dyn.Resource(nodePoolResource).Get(ctx, pool, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		limit, err := nestedQuantity(obj, "spec", "limits", "cpu")
		if err != nil || limit == nil {
			return "", err
		}
		used, err := nestedQuantity(obj, "status", "resources", "cpu")
		if err != nil {
			return "", err
		}
		free := limit.MilliValue()
		if used != nil {
			free -= used.MilliValue()
		}
		if free < minNodeCores*1000 {
			return "the Cloud node pool is at its CPU limit", nil
		}
		return "", nil
	}
}

func nestedQuantity(obj *unstructured.Unstructured, fields ...string) (*resource.Quantity, error) {
	raw, found, err := unstructured.NestedFieldNoCopy(obj.Object, fields...)
	if err != nil || !found {
		return nil, err
	}
	q, err := resource.ParseQuantity(fmt.Sprint(raw))
	if err != nil {
		return nil, fmt.Errorf("%v: %w", fields, err)
	}
	return &q, nil
}
