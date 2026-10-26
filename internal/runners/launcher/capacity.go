package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

const (
	capacityLabel    = "sparkwing.dev/capacity-request"
	capacityPrefix   = "sw-capacity-"
	demandAnnotation = "sparkwing.dev/capacity-demand"
)

type capacityDemand struct {
	Binding  CloudJobBinding `json:"binding"`
	CPUCores float64         `json:"cpu_cores"`
	Memory   int64           `json:"memory_bytes"`
}

func demandFor(award CloudJobAward) capacityDemand {
	return capacityDemand{Binding: award.Binding, CPUCores: award.CPUCores, Memory: award.Memory}
}

// safety: the name carries the award generation, so a request is never rebound
// and cleanup of a stale award cannot reach a newer award's request.
func capacityName(b CloudJobBinding) string {
	return capacityPrefix + strings.TrimPrefix(jobName(b), "sw-")
}

func teamTolerations(namespace, team string) []corev1.Toleration {
	return []corev1.Toleration{{Key: "sparkwing.dev/cpu-band", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}, {Key: TeamNodeLabel, Value: TeamNodeValue(namespace, team), Operator: corev1.TolerationOpEqual, Effect: corev1.TaintEffectNoSchedule}}
}

// safety: the pod pends until Karpenter adds a node from the team's own pool,
// and carries nothing from the award beyond its unit fence, size and deadline.
func capacityPod(cfg CloudJobConfig, d capacityDemand) (*corev1.Pod, error) {
	budget, err := cloudBudget(cfg, CloudJobAward{CPUCores: d.CPUCores, Memory: d.Memory})
	if err != nil {
		return nil, err
	}
	deadline, err := cloudDeadline(time.Time{}, d.Binding)
	if err != nil {
		return nil, err
	}
	demand, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	b := d.Binding
	requests := budget.DeepCopy()
	requests[corev1.ResourceEphemeralStorage] = *resource.NewQuantity(limits.JobScratchBytes, resource.BinarySI)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: capacityName(b), Namespace: cfg.Namespace,
			Labels:      map[string]string{"app": "sparkwing-job", "app.kubernetes.io/managed-by": managedByLauncher, capacityLabel: "true", TeamLabel: TeamNodeValue(cfg.Namespace, b.Team)},
			Annotations: map[string]string{deadlineAnnotation: deadline.Format(time.RFC3339Nano), demandAnnotation: string(demand)},
		},
		Spec: corev1.PodSpec{
			Tolerations: teamTolerations(cfg.Namespace, b.Team), NodeSelector: map[string]string{TeamNodeLabel: TeamNodeValue(cfg.Namespace, b.Team)},
			// safety: a request that fit on another run's warm node would hold it without ever
			// being placed there, and Karpenter would add no node for it.
			Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: usedRunLabel, Operator: corev1.NodeSelectorOpDoesNotExist}}},
				{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: usedRunLabel, Operator: corev1.NodeSelectorOpIn, Values: []string{runNodeValue(cfg.Namespace, b.RunID)}}}},
			}}}},
			ServiceAccountName: ServiceAccount, AutomountServiceAccountToken: ptr(false), EnableServiceLinks: ptr(false), RestartPolicy: corev1.RestartPolicyNever,
			TerminationGracePeriodSeconds: ptr(int64(1)),
			SecurityContext:               probePodSecurity(),
			Containers: []corev1.Container{{
				Name: "capacity", Image: cfg.RunnerImage, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{JobBinary}, Args: []string{"version"},
				Resources:       corev1.ResourceRequirements{Requests: requests, Limits: requests.DeepCopy()},
				SecurityContext: capacitySecurity(),
			}},
		},
	}, nil
}

func recordedDemand(pod *corev1.Pod) (capacityDemand, error) {
	var d capacityDemand
	if err := json.Unmarshal([]byte(pod.Annotations[demandAnnotation]), &d); err != nil {
		return d, &podMismatch{message: fmt.Sprintf("capacity Pod %s has no readable demand", pod.Name)}
	}
	return d, nil
}

func (l *Launcher) verifyCapacityPod(pod *corev1.Pod, d capacityDemand) error {
	expected, err := capacityPod(l.Config, d)
	if err == nil && (expected.Name != pod.Name || pod.Namespace != l.Config.Namespace || len(pod.OwnerReferences) != 0 || pod.Annotations[demandAnnotation] != expected.Annotations[demandAnnotation]) {
		err = errors.New("capacity Pod identity does not match its award")
	}
	if err == nil {
		err = awardPod(&corev1.PodTemplateSpec{ObjectMeta: expected.ObjectMeta, Spec: expected.Spec}, &corev1.PodTemplateSpec{ObjectMeta: pod.ObjectMeta, Spec: pod.Spec})
	}
	if err != nil {
		return &podMismatch{message: fmt.Sprintf("capacity Pod %s failed verification: %v", pod.Name, err)}
	}
	return nil
}

func (l *Launcher) authenticateCapacity(pod *corev1.Pod, state CloudJobState) error {
	if state.Resources == nil {
		return &podMismatch{message: fmt.Sprintf("capacity Pod %s has no controller allocation", pod.Name)}
	}
	return l.verifyCapacityPod(pod, capacityDemand{Binding: state.Binding, CPUCores: state.Resources.Cores, Memory: state.Resources.MemoryBytes})
}

func (l *Launcher) deleteCapacityPod(ctx context.Context, pod *corev1.Pod) error {
	err := l.Kube.CoreV1().Pods(l.Config.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID}})
	if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
		return nil
	}
	return err
}

func (l *Launcher) requestCapacity(ctx context.Context, award CloudJobAward) error {
	d := demandFor(award)
	pod, err := capacityPod(l.Config, d)
	if err != nil {
		return err
	}
	pods := l.Kube.CoreV1().Pods(l.Config.Namespace)
	admitted, err := pods.Create(ctx, pod, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		admitted, err = pods.Get(ctx, pod.Name, metav1.GetOptions{})
	}
	if err != nil {
		return err
	}
	if err := l.verifyCapacityPod(admitted, d); err != nil {
		return errors.Join(err, l.deleteCapacityPod(ctx, admitted))
	}
	return nil
}

func (l *Launcher) capacityPods(ctx context.Context) ([]corev1.Pod, error) {
	pods, err := l.Kube.CoreV1().Pods(l.Config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: capacityLabel + "=true"})
	if err != nil {
		return nil, err
	}
	return pods.Items, nil
}

func demandDeadline(b CloudJobBinding) time.Time {
	deadline, err := cloudDeadline(time.Time{}, b)
	if err != nil {
		return time.Time{}
	}
	return deadline
}

// safety: only awards this launcher holds are renewed, so a restarted launcher
// lets an abandoned award lapse and its request go with it.
func (l *Launcher) syncCapacity(ctx context.Context) error {
	pods, err := l.capacityPods(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for i := range pods {
		pod := &pods[i]
		if pod.DeletionTimestamp != nil {
			continue
		}
		d, err := recordedDemand(pod)
		held := l.held(pod.Name)
		if held != nil && err == nil {
			err = l.verifyCapacityPod(pod, demandFor(*held))
		}
		expired := err == nil && !time.Now().Before(demandDeadline(d.Binding))
		if expired {
			l.forget(pod.Name)
		}
		if err != nil || expired {
			errs = append(errs, err, l.deleteCapacityPod(ctx, pod))
			continue
		}
		name := jobName(d.Binding)
		observation := CloudJobObservation{Binding: d.Binding, JobName: name, Capacity: &CloudCapacityObservation{PodUID: string(pod.UID)}, Unheld: held == nil || !sameBinding(held.Binding, d.Binding)}
		states, err := l.retirements(ctx, []CloudJobObservation{observation})
		if err != nil && !capacityIdentityRefused(err) {
			errs = append(errs, err)
			continue
		}
		if err != nil {
			l.logger().Warn("controller refused capacity Pod identity", "pod", pod.Name, "uid", pod.UID, "error", err)
		} else if state := states[name]; !state.Delete {
			if err := l.authenticateCapacity(pod, state); err != nil {
				errs = append(errs, err)
			} else {
				continue
			}
		}
		l.forget(pod.Name)
		errs = append(errs, l.deleteCapacityPod(ctx, pod))
	}
	return errors.Join(errs...)
}

func capacityIdentityRefused(err error) bool {
	var failure *apierr.Error
	return errors.As(err, &failure) && failure.Code == apierr.LockHeld || bindingRefused(err)
}

func (l *Launcher) wait(award *CloudJobAward) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.waiting == nil {
		l.waiting = map[string]*CloudJobAward{}
	}
	l.waiting[capacityName(award.Binding)] = award
}

func (l *Launcher) forget(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.waiting, name)
}

func (l *Launcher) held(name string) *CloudJobAward {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.waiting[name]
}

func (l *Launcher) waitingAwards() []*CloudJobAward {
	l.mu.Lock()
	defer l.mu.Unlock()
	awards := make([]*CloudJobAward, 0, len(l.waiting))
	for _, award := range l.waiting {
		awards = append(awards, award)
	}
	return awards
}

func (l *Launcher) releaseCapacity(ctx context.Context, b CloudJobBinding) error {
	name := capacityName(b)
	l.forget(name)
	pod, err := l.Kube.CoreV1().Pods(l.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return l.deleteCapacityPod(ctx, pod)
}

// capacitySecurity is the probe profile with a read-only root filesystem.
func capacitySecurity() *corev1.SecurityContext {
	sc := probeSecurity()
	sc.ReadOnlyRootFilesystem = ptr(true)
	return sc
}
