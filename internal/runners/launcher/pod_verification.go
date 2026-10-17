package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

func (l *Launcher) verifyJobPods(ctx context.Context, job *batchv1.Job) error {
	expected := job.DeepCopy()
	if err := json.Unmarshal([]byte(job.Annotations[templateAnnotation]), &expected.Spec.Template); err != nil {
		return fmt.Errorf("invalid requested Cloud template: %w", err)
	}
	p, q := expected.Spec.Template.Spec.DeepCopy(), job.Spec.Template.Spec.DeepCopy()
	defaultPodSpec(p)
	defaultPodSpec(q)
	if !equality.Semantic.DeepEqual(p, q) || !equality.Semantic.DeepEqual(expected.Spec.Template.Annotations, job.Spec.Template.Annotations) {
		return errors.New("Cloud Job template changed after creation")
	}
	pods, err := l.Kube.CoreV1().Pods(l.Config.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		owner := metav1.GetControllerOf(pod)
		if pod.Labels[JobLabel] != job.Name && !(owner != nil && owner.UID == job.UID) && pod.GenerateName != job.Name+"-" {
			continue
		}
		if err := validateJobPod(expected, pod); err != nil {
			return fmt.Errorf("Cloud Pod %s failed verification: %w", pods.Items[i].Name, err)
		}
	}
	return nil
}

func validateJobPod(job *batchv1.Job, pod *corev1.Pod) error {
	owner := metav1.GetControllerOf(pod)
	if pod.Namespace != job.Namespace || owner == nil || owner.APIVersion != "batch/v1" || owner.Kind != "Job" || owner.Name != job.Name || owner.UID != job.UID {
		return errors.New("Pod is not owned by the created Job")
	}
	labels := pod.Labels
	for key, value := range job.Spec.Template.Labels {
		if labels[key] != value {
			return errors.New("Pod lost its Job labels")
		}
	}
	for key, value := range labels {
		if job.Spec.Template.Labels[key] == value {
			continue
		}
		switch key {
		case "batch.kubernetes.io/controller-uid", "controller-uid":
			if value == string(job.UID) {
				continue
			}
		case "batch.kubernetes.io/job-name", "job-name":
			if value == job.Name {
				continue
			}
		}
		return errors.New("Pod has unexpected labels")
	}
	annotations := pod.Annotations
	for key, value := range job.Spec.Template.Annotations {
		if annotations[key] != value {
			return errors.New("Pod lost its Job annotations")
		}
	}
	for key, value := range annotations {
		if job.Spec.Template.Annotations[key] != value && key != "vpc.amazonaws.com/pod-eni" {
			return errors.New("Pod has unexpected annotations")
		}
	}
	p, q := job.Spec.Template.Spec.DeepCopy(), pod.Spec.DeepCopy()
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
			for _, mount := range b.VolumeMounts[len(a.VolumeMounts):] {
				if !eksMount(mount) {
					return errors.New("Pod has unexpected mount injection")
				}
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
	for _, v := range q.Volumes[len(p.Volumes):] {
		if !eksTokenVolume(v) {
			return errors.New("Pod has unexpected volume injection")
		}
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
	case "AWS_CONTAINER_CREDENTIALS_FULL_URI":
		return e.Value == "http://169.254.170.23/v1/credentials" || e.Value == "http://[fd00:ec2::23]/v1/credentials"
	case "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE":
		return e.Value == "/var/run/secrets/pods.eks.amazonaws.com/serviceaccount/eks-pod-identity-token"
	case "AWS_WEB_IDENTITY_TOKEN_FILE":
		return e.Value == "/var/run/secrets/eks.amazonaws.com/serviceaccount/token"
	case "AWS_ROLE_ARN":
		return strings.HasPrefix(e.Value, "arn:aws:iam::") && strings.Contains(e.Value, ":role/")
	case "AWS_REGION", "AWS_DEFAULT_REGION":
		return e.Value != "" && len(validation.IsDNS1123Label(e.Value)) == 0
	case "AWS_STS_REGIONAL_ENDPOINTS":
		return e.Value == "regional"
	}
	return false
}

func eksMount(m corev1.VolumeMount) bool {
	path := ""
	switch m.Name {
	case "aws-iam-token":
		path = "/var/run/secrets/eks.amazonaws.com/serviceaccount"
	case "eks-pod-identity-token":
		path = "/var/run/secrets/pods.eks.amazonaws.com/serviceaccount"
	default:
		return false
	}
	return equality.Semantic.DeepEqual(m, corev1.VolumeMount{Name: m.Name, MountPath: path, ReadOnly: true})
}

func eksTokenVolume(v corev1.Volume) bool {
	audience, path := "", ""
	switch v.Name {
	case "aws-iam-token":
		audience, path = "sts.amazonaws.com", "token"
	case "eks-pod-identity-token":
		audience, path = "pods.eks.amazonaws.com", "eks-pod-identity-token"
	default:
		return false
	}
	projected := v.Projected
	if projected == nil || len(projected.Sources) != 1 || projected.Sources[0].ServiceAccountToken == nil {
		return false
	}
	token := projected.Sources[0].ServiceAccountToken
	if token.ExpirationSeconds == nil || *token.ExpirationSeconds < 600 {
		return false
	}
	expected := corev1.Volume{Name: v.Name, VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{DefaultMode: ptr(int32(420)), Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Audience: audience, Path: path, ExpirationSeconds: token.ExpirationSeconds}}}}}}
	return equality.Semantic.DeepEqual(v, expected)
}
