package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

type podMismatch struct {
	message string
	missing bool
}

func (e *podMismatch) Error() string { return e.message }

func (l *Launcher) waitForJobPod(ctx context.Context, job *batchv1.Job) error {
	err := wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		err := l.verifyJobPods(ctx, job)
		var mismatch *podMismatch
		if errors.As(err, &mismatch) && mismatch.missing {
			return false, nil
		}
		return err == nil, err
	})
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return &podMismatch{message: "active Cloud Job has no owned Pod"}
	}
	return err
}

func (l *Launcher) verificationFailure(ctx context.Context, job *batchv1.Job, binding CloudJobBinding, r CloudNodeReservation, err error, acknowledge bool) error {
	var mismatch *podMismatch
	if !errors.As(err, &mismatch) {
		return err
	}
	if acknowledge {
		return errors.Join(err, l.rejectJob(ctx, job, binding, r))
	}
	_, containErr := l.containJob(ctx, job, binding, r)
	return errors.Join(err, containErr)
}

func (l *Launcher) verifyJobPods(ctx context.Context, job *batchv1.Job) error {
	expected := job.DeepCopy()
	if err := json.Unmarshal([]byte(job.Annotations[templateAnnotation]), &expected.Spec.Template); err != nil {
		return &podMismatch{message: fmt.Sprintf("invalid requested Cloud template: %v", err)}
	}
	p, q := expected.Spec.Template.Spec.DeepCopy(), job.Spec.Template.Spec.DeepCopy()
	defaultPodSpec(p)
	defaultPodSpec(q)
	if !equality.Semantic.DeepEqual(p, q) || !equality.Semantic.DeepEqual(expected.Spec.Template.Annotations, job.Spec.Template.Annotations) {
		return &podMismatch{message: "Cloud Job template changed after creation"}
	}
	pods, err := l.Kube.CoreV1().Pods(l.Config.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	count := 0
	for i := range pods.Items {
		pod := &pods.Items[i]
		owner := metav1.GetControllerOf(pod)
		if pod.Labels[JobLabel] != job.Name && !(owner != nil && owner.UID == job.UID) && pod.GenerateName != job.Name+"-" {
			continue
		}
		count++
		requested := expected.DeepCopy()
		if err := defaultPriority(ctx, l.Kube, &requested.Spec.Template.Spec); err != nil {
			return err
		}
		if err := validateJobPod(requested, pod); err != nil {
			return &podMismatch{message: fmt.Sprintf("Cloud Pod %s failed verification: %v", pod.Name, err)}
		}
	}
	if (job.Spec.Suspend == nil || !*job.Spec.Suspend) && !jobFinished(*job) && count != 1 {
		return &podMismatch{message: fmt.Sprintf("active Cloud Job requires one owned Pod, found %d", count), missing: count == 0}
	}
	return nil
}

func defaultPriority(ctx context.Context, kube kubernetes.Interface, spec *corev1.PodSpec) error {
	if spec.PriorityClassName != "" {
		return nil
	}
	classes, err := kube.SchedulingV1().PriorityClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, class := range classes.Items {
		if class.GlobalDefault && (spec.Priority == nil || class.Value < *spec.Priority) {
			spec.PriorityClassName, spec.Priority, spec.PreemptionPolicy = class.Name, ptr(class.Value), class.PreemptionPolicy
		}
	}
	return nil
}

func validateJobPod(job *batchv1.Job, pod *corev1.Pod) error {
	owner := metav1.GetControllerOf(pod)
	if pod.Namespace != job.Namespace || owner == nil || owner.APIVersion != "batch/v1" || owner.Kind != "Job" || owner.Name != job.Name || owner.UID != job.UID {
		return errors.New("Pod is not owned by the created Job")
	}
	return verifyAdmittedPod(job.Spec.Template, pod, func(key, value string) bool {
		switch key {
		case "batch.kubernetes.io/controller-uid", "controller-uid":
			return value == string(job.UID)
		case "batch.kubernetes.io/job-name", "job-name":
			return value == job.Name
		}
		return false
	})
}

// safety: every launcher-created Pod kind passes this one comparison, so the
// admitted object differs from the request only by API defaults, the named EKS
// injections, owner labels and a scheduler-chosen node.
func verifyAdmittedPod(requested corev1.PodTemplateSpec, pod *corev1.Pod, ownerLabel func(key, value string) bool) error {
	labels := pod.Labels
	for key, value := range requested.Labels {
		if labels[key] != value {
			return errors.New("Pod lost its requested labels")
		}
	}
	for key, value := range labels {
		if requested.Labels[key] != value && (ownerLabel == nil || !ownerLabel(key, value)) {
			return errors.New("Pod has unexpected labels")
		}
	}
	annotations := pod.Annotations
	for key, value := range requested.Annotations {
		if annotations[key] != value {
			return errors.New("Pod lost its requested annotations")
		}
	}
	for key, value := range annotations {
		if key == "vpc.amazonaws.com/pod-eni" {
			if err := validateENI(value); err != nil {
				return err
			}
		}
		if requested.Annotations[key] != value && key != "vpc.amazonaws.com/pod-eni" {
			return errors.New("Pod has unexpected annotations")
		}
	}
	p, q := requested.Spec.DeepCopy(), pod.Spec.DeepCopy()
	if p.NodeName == "" {
		q.NodeName = ""
	}
	if err := removeEKSMutations(p, q); err != nil {
		return err
	}
	defaultPodSpec(p)
	defaultPodSpec(q)
	if !equality.Semantic.DeepEqual(p, q) {
		return errors.New("Pod differs from the awarded spec")
	}
	return nil
}

func defaultPodSpec(p *corev1.PodSpec) {
	if p.DNSPolicy == "" {
		p.DNSPolicy = corev1.DNSClusterFirst
	}
	if p.SchedulerName == "" {
		p.SchedulerName = corev1.DefaultSchedulerName
	}
	if p.TerminationGracePeriodSeconds == nil {
		p.TerminationGracePeriodSeconds = ptr(int64(30))
	}
	if p.DeprecatedServiceAccount == "" {
		p.DeprecatedServiceAccount = p.ServiceAccountName
	}
	if p.PreemptionPolicy == nil {
		p.PreemptionPolicy = ptr(corev1.PreemptLowerPriority)
	}
	if p.Priority == nil {
		p.Priority = ptr(int32(0))
	}
	for _, cs := range [][]corev1.Container{p.Containers, p.InitContainers} {
		for i := range cs {
			c := &cs[i]
			if c.TerminationMessagePath == "" {
				c.TerminationMessagePath = corev1.TerminationMessagePathDefault
			}
			if c.TerminationMessagePolicy == "" {
				c.TerminationMessagePolicy = corev1.TerminationMessageReadFile
			}
			defaultProbe(c.StartupProbe)
		}
	}
	p.Tolerations = slices.DeleteFunc(p.Tolerations, func(t corev1.Toleration) bool {
		return (t.Key == "node.kubernetes.io/not-ready" || t.Key == "node.kubernetes.io/unreachable") && t.Operator == corev1.TolerationOpExists && t.Effect == corev1.TaintEffectNoExecute && t.Value == "" && t.TolerationSeconds != nil && *t.TolerationSeconds == 300
	})
}

func removeEKSMutations(p, q *corev1.PodSpec) error {
	if len(p.Containers) != len(q.Containers) || len(p.InitContainers) != len(q.InitContainers) {
		return errors.New("Pod container count changed")
	}
	for _, pair := range [][2][]corev1.Container{{p.Containers, q.Containers}, {p.InitContainers, q.InitContainers}} {
		for i := range pair[0] {
			a, b := &pair[0][i], &pair[1][i]
			if len(b.Env) < len(a.Env) || len(b.VolumeMounts) < len(a.VolumeMounts) {
				return errors.New("Pod lost environment or mounts")
			}
			seen := map[string]bool{}
			for _, env := range b.Env[len(a.Env):] {
				if seen[env.Name] || !eksEnv(env) {
					return errors.New("Pod has unexpected environment injection")
				}
				seen[env.Name] = true
			}
			b.Env = b.Env[:len(a.Env)]
			if len(b.VolumeMounts) != len(a.VolumeMounts) {
				return errors.New("Pod has unexpected mount injection")
			}
			b.VolumeMounts = b.VolumeMounts[:len(a.VolumeMounts)]
			for _, resources := range []corev1.ResourceList{b.Resources.Requests, b.Resources.Limits} {
				if v, ok := resources["vpc.amazonaws.com/pod-eni"]; ok {
					if v.CmpInt64(1) != 0 {
						return errors.New("Pod ENI request must be one")
					}
					delete(resources, "vpc.amazonaws.com/pod-eni")
				}
			}
			if len(b.Resources.Requests) == 0 && a.Resources.Requests == nil {
				b.Resources.Requests = nil
			}
			if len(b.Resources.Limits) == 0 && a.Resources.Limits == nil {
				b.Resources.Limits = nil
			}
		}
	}
	if len(q.Volumes) < len(p.Volumes) {
		return errors.New("Pod lost volumes")
	}
	if len(q.Volumes) != len(p.Volumes) {
		return errors.New("Pod has unexpected volume injection")
	}
	q.Volumes = q.Volumes[:len(p.Volumes)]
	for _, gate := range q.ReadinessGates {
		suffix, ok := strings.CutPrefix(string(gate.ConditionType), "target-health.elbv2.k8s.aws/")
		if !ok || suffix == "" || len(validation.IsQualifiedName(string(gate.ConditionType))) != 0 {
			return errors.New("Pod has unexpected readiness gates")
		}
	}
	q.ReadinessGates = nil
	return nil
}

func eksEnv(e corev1.EnvVar) bool {
	if e.ValueFrom != nil {
		return false
	}
	switch e.Name {
	case "AWS_REGION", "AWS_DEFAULT_REGION":
		return e.Value != "" && len(validation.IsDNS1123Label(e.Value)) == 0
	case "AWS_STS_REGIONAL_ENDPOINTS":
		return e.Value == "regional"
	}
	return false
}
