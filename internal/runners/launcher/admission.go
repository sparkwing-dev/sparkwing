package launcher

import (
	"errors"
	"slices"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
)

func validateAdmittedJob(expected, admitted *batchv1.Job) error {
	if admitted == nil || admitted.Name != expected.Name || admitted.Namespace != expected.Namespace ||
		admitted.DeletionTimestamp != nil || !slices.Contains(admitted.Finalizers, cleanupFinalizer) ||
		expected.Annotations[bootstrapAnnotation] != admitted.Annotations[bootstrapAnnotation] ||
		!equality.Semantic.DeepDerivative(expected.Labels, admitted.Labels) ||
		!equality.Semantic.DeepDerivative(expected.Annotations, admitted.Annotations) ||
		!equality.Semantic.DeepDerivative(expected.Spec.Template.Labels, admitted.Spec.Template.Labels) ||
		!equality.Semantic.DeepDerivative(expected.Spec.Template.Annotations, admitted.Spec.Template.Annotations) {
		return errors.New("Kubernetes changed Cloud Job teardown ownership")
	}
	if !equality.Semantic.DeepEqual(expected.Spec.Suspend, admitted.Spec.Suspend) ||
		(admitted.Spec.Parallelism != nil && *admitted.Spec.Parallelism != 1) ||
		(admitted.Spec.Completions != nil && *admitted.Spec.Completions != 1) ||
		!equality.Semantic.DeepEqual(expected.Spec.BackoffLimit, admitted.Spec.BackoffLimit) ||
		!equality.Semantic.DeepEqual(expected.Spec.PodFailurePolicy, admitted.Spec.PodFailurePolicy) ||
		!equality.Semantic.DeepEqual(expected.Spec.ActiveDeadlineSeconds, admitted.Spec.ActiveDeadlineSeconds) {
		return errors.New("Kubernetes changed Cloud Job execution or deadline policy")
	}
	p, q := expected.Spec.Template.Spec.DeepCopy(), admitted.Spec.Template.Spec.DeepCopy()
	defaultPodSpec(p)
	defaultPodSpec(q)
	if !equality.Semantic.DeepEqual(p, q) {
		return errors.New("Kubernetes changed Cloud Job placement, containers or isolation")
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
}

func defaultProbe(p *corev1.Probe) {
	if p == nil {
		return
	}
	if p.TimeoutSeconds == 0 {
		p.TimeoutSeconds = 1
	}
	if p.SuccessThreshold == 0 {
		p.SuccessThreshold = 1
	}
}

func validateAdmittedBootstrap(expected, admitted *corev1.Secret) error {
	if admitted == nil || admitted.Name != expected.Name || admitted.Namespace != expected.Namespace ||
		admitted.DeletionTimestamp != nil || admitted.Type != expected.Type || len(admitted.StringData) != 0 ||
		!equality.Semantic.DeepEqual(expected.Immutable, admitted.Immutable) ||
		!equality.Semantic.DeepEqual(expected.Data, admitted.Data) ||
		!equality.Semantic.DeepEqual(expected.OwnerReferences, admitted.OwnerReferences) {
		return errors.New("Kubernetes changed Cloud bootstrap delivery or teardown ownership")
	}
	return nil
}
