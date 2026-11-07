package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

const (
	capacityLabel    = "sparkwing.dev/capacity-request"
	capacityPrefix   = "sw-capacity-"
	demandAnnotation = "sparkwing.dev/capacity-demand"
)

var errCapacityUnconfigured = errors.New("cloud capacity is not configured")

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

// capacityPodSecurity runs a capacity request unprivileged with the runtime seccomp profile.
func capacityPodSecurity() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{RunAsUser: ptr(int64(65534)), RunAsGroup: ptr(int64(65534)), RunAsNonRoot: ptr(true), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
}

func cloudTolerations() []corev1.Toleration {
	return []corev1.Toleration{{Key: "sparkwing.dev/cpu-band", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}, {Key: DedicatedTaint, Value: "true", Operator: corev1.TolerationOpEqual, Effect: corev1.TaintEffectNoSchedule}}
}

// safety: requests carry only a unit fence, size and deadline. They select fresh
// shared capacity or this run's used node, never another run's node.
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
			Tolerations: cloudTolerations(), NodeSelector: map[string]string{CloudClassLabel: cpuClass(d.CPUCores), PoolNamespaceLabel: cfg.Namespace},
			// safety: a request that fit on another run's warm node would hold it without ever
			// being placed there, and Karpenter would add no node for it.
			Affinity: &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: usedRunLabel, Operator: corev1.NodeSelectorOpDoesNotExist}, {Key: usedTeamLabel, Operator: corev1.NodeSelectorOpDoesNotExist}, {Key: TeamNodeLabel, Operator: corev1.NodeSelectorOpDoesNotExist}}},
					{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: usedRunLabel, Operator: corev1.NodeSelectorOpIn, Values: []string{runNodeValue(cfg.Namespace, b.RunID)}}, {Key: usedTeamLabel, Operator: corev1.NodeSelectorOpIn, Values: []string{TeamNodeValue(cfg.Namespace, b.Team)}}, {Key: TeamNodeLabel, Operator: corev1.NodeSelectorOpIn, Values: []string{TeamNodeValue(cfg.Namespace, b.Team)}}}},
				}}},
				// safety: Karpenter provisions a node per team's demand, so one fresh node
				// never carries two teams' requests that each refuse to bind it.
				PodAntiAffinity: &corev1.PodAntiAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
					TopologyKey: corev1.LabelHostname,
					LabelSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
						{Key: TeamLabel, Operator: metav1.LabelSelectorOpExists},
						{Key: TeamLabel, Operator: metav1.LabelSelectorOpNotIn, Values: []string{TeamNodeValue(cfg.Namespace, b.Team)}},
					}},
				}}},
			},
			ServiceAccountName: ServiceAccount, AutomountServiceAccountToken: ptr(false), EnableServiceLinks: ptr(false), RestartPolicy: corev1.RestartPolicyNever,
			TerminationGracePeriodSeconds: ptr(int64(1)),
			SecurityContext:               capacityPodSecurity(),
			Containers: []corev1.Container{{
				// perf: sleep as PID 1 ignores SIGTERM, so every delete waited out the kubelet's
				// 2s minimum grace on the cold-start path. The trapped shell exits at once and the
				// kubelet still frees the requests before the API removes the Pod.
				Name: "capacity", Image: cfg.RunnerImage, ImagePullPolicy: corev1.PullIfNotPresent, Command: capacityCommand, Args: []string{strconv.FormatFloat(max(0, time.Until(deadline).Seconds()), 'f', 9, 64)},
				Resources:       corev1.ResourceRequirements{Requests: requests, Limits: requests.DeepCopy()},
				SecurityContext: capacitySecurity(),
			}},
		},
	}, nil
}

// safety: the deadline travels as $1, never spliced into the script.
var capacityCommand = []string{"/bin/sh", "-c", `trap 'exit 0' TERM; /usr/bin/sleep "$1" & wait`, "capacity"}

func recordedDemand(pod *corev1.Pod) (capacityDemand, error) {
	var d capacityDemand
	if err := json.Unmarshal([]byte(pod.Annotations[demandAnnotation]), &d); err != nil {
		return d, &podMismatch{message: fmt.Sprintf("capacity Pod %s has no readable demand", pod.Name)}
	}
	return d, nil
}

func (l *Launcher) verifyCapacityPod(pod *corev1.Pod, d capacityDemand) error {
	expected, err := capacityPod(l.Config, d)
	if err == nil && (expected.Name != pod.Name || pod.Namespace != l.Config.Namespace || len(pod.OwnerReferences) != 0 || pod.Annotations[demandAnnotation] != expected.Annotations[demandAnnotation] || !equality.Semantic.DeepEqual(expected.Spec.Affinity, pod.Spec.Affinity)) {
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

func (l *Launcher) awaitCapacity(ctx context.Context, award CloudJobAward) error {
	budget, err := cloudBudget(l.Config, award)
	if err != nil && !errors.Is(err, errCapacityUnconfigured) {
		return err
	}
	if err != nil {
		ceiling := &match.Resources{Cores: l.Config.CPUCeiling, MemoryBytes: l.Config.MemoryCeiling}
		if err := l.failCapacity(ctx, award, CloudCapacityObservation{Unconfigured: true, Ceiling: ceiling}); err != nil {
			return err
		}
		// safety: an oversized request is the customer's choice, not a launcher fault, so it stays below WARN.
		l.logger().Info("Cloud request exceeds the launcher resource ceilings; failed the award and refunded its hold", "team", award.Binding.Team, "requested_class", cpuClass(award.CPUCores), "requested_cpu", award.CPUCores, "available_cpu", l.Config.CPUCeiling, "requested_memory", award.Memory, "available_memory", l.Config.MemoryCeiling, "namespace", l.Config.Namespace, "support_reference", award.Binding.RunID)
		return l.releaseCapacity(ctx, award.Binding)
	}
	configured, err := l.Boundary.CapacityConfigured(ctx, budget)
	if err != nil {
		return err
	}
	if !configured {
		if err := l.failCapacity(ctx, award, CloudCapacityObservation{Unconfigured: true}); err != nil {
			return err
		}
		l.logger().Error("Cloud capacity is not configured; configure the shared NodePool for the requested hardware class", "team", award.Binding.Team, "requested_class", cpuClass(award.CPUCores), "namespace", l.Config.Namespace, "support_reference", award.Binding.RunID)
		return l.releaseCapacity(ctx, award.Binding)
	}
	if l.breakerClosed(ctx) {
		pod, err := l.Kube.CoreV1().Pods(l.Config.Namespace).Get(ctx, capacityName(award.Binding), metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		// perf: scheduled demand already has a node to pay for, even before that node is Ready.
		if apierrors.IsNotFound(err) || pod.Spec.NodeName == "" {
			if err := l.failCapacity(ctx, award, CloudCapacityObservation{Unavailable: true}); err != nil {
				return err
			}
			l.logger().Error("Cloud budget breaker is closed; failed a waiting award instead of requesting a job node", "team", award.Binding.Team, "requested_class", cpuClass(award.CPUCores), "namespace", l.Config.Namespace, "support_reference", award.Binding.RunID)
			return l.releaseCapacity(ctx, award.Binding)
		}
	}
	if err := l.requestCapacity(ctx, award); err != nil {
		return err
	}
	return ErrNoDedicatedNode
}

// safety: only a readable closed flag trips, so a read fault never fails a run.
func (l *Launcher) breakerClosed(ctx context.Context) bool {
	if l.Breaker == nil {
		return false
	}
	closed, err := l.Breaker(ctx)
	if err != nil {
		l.logger().Warn("Cloud budget state unreadable; treating the breaker as open", "error", err)
		return false
	}
	return closed
}

func (l *Launcher) failCapacity(ctx context.Context, award CloudJobAward, capacity CloudCapacityObservation) error {
	states, err := l.retirements(ctx, []CloudJobObservation{{Binding: award.Binding, JobName: jobName(award.Binding), Capacity: &capacity}})
	if err != nil {
		return err
	}
	if !states[jobName(award.Binding)].Delete {
		return errors.New("controller deferred a Cloud capacity failure")
	}
	return nil
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
	if admitted.DeletionTimestamp != nil {
		return nil
	}
	if err := l.verifyCapacityPod(admitted, d); err != nil {
		return errors.Join(err, l.deleteCapacityPod(ctx, admitted))
	}
	// safety: only displacement needs fresh demand. Retrying an incompatible pool
	// would keep provisioning nodes that this award can never reserve.
	displaced, err := l.capacityDisplaced(ctx, admitted, d.Binding)
	if err != nil {
		return err
	}
	if displaced {
		return l.deleteCapacityPod(ctx, admitted)
	}
	if admitted.Spec.NodeName == "" {
		return nil
	}
	// safety: a replacement would bind the same emptied node again, so a refused node
	// is reported on every pass for an operator to fix its pool instead.
	refusal, err := l.Boundary.CapacityNodeRefusal(ctx, admitted.Spec.NodeName, d.Binding.Team)
	if err == nil && refusal != "" {
		l.logger().Warn("capacity Pod is bound to a node the launcher cannot reserve; fix the node's NodePool or EC2NodeClass", "pod", admitted.Name, "node", admitted.Spec.NodeName, "team", d.Binding.Team, "node_class", l.Config.NodeClassName, "reason", refusal, "support_reference", d.Binding.RunID)
	}
	return err
}

func (l *Launcher) capacityDisplaced(ctx context.Context, pod *corev1.Pod, binding CloudJobBinding) (bool, error) {
	if pod.Spec.NodeName == "" {
		return false, nil
	}
	node, err := l.Kube.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	team := TeamNodeValue(l.Config.Namespace, binding.Team)
	for _, key := range []string{TeamNodeLabel, usedTeamLabel} {
		if value := node.Labels[key]; value != "" && value != team {
			return true, nil
		}
	}
	if run := node.Labels[usedRunLabel]; run != "" && run != runNodeValue(l.Config.Namespace, binding.RunID) {
		return true, nil
	}
	pods, err := l.Kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node.Name})
	if err != nil {
		return false, err
	}
	for _, other := range pods.Items {
		if other.Spec.NodeName == node.Name && other.Labels[TeamLabel] != "" && other.Labels[TeamLabel] != team {
			return true, nil
		}
	}
	return false, nil
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

// capacitySecurity drops every capability and privilege escalation and keeps
// the root filesystem read-only.
func capacitySecurity() *corev1.SecurityContext {
	return &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, ReadOnlyRootFilesystem: ptr(true)}
}
