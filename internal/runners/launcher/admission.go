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
	if admitted.Spec.Suspend == nil || !*admitted.Spec.Suspend ||
		(admitted.Spec.Parallelism != nil && *admitted.Spec.Parallelism != 1) ||
		(admitted.Spec.Completions != nil && *admitted.Spec.Completions != 1) ||
		!equality.Semantic.DeepEqual(expected.Spec.BackoffLimit, admitted.Spec.BackoffLimit) ||
		!equality.Semantic.DeepEqual(expected.Spec.PodFailurePolicy, admitted.Spec.PodFailurePolicy) ||
		!equality.Semantic.DeepEqual(expected.Spec.ActiveDeadlineSeconds, admitted.Spec.ActiveDeadlineSeconds) {
		return errors.New("Kubernetes changed Cloud Job execution or deadline policy")
	}
	p, q := expected.Spec.Template.Spec.DeepCopy(), admitted.Spec.Template.Spec.DeepCopy()
	if !equality.Semantic.DeepEqual(p.Resources, q.Resources) {
		return errors.New("Kubernetes did not retain the shared pod resource budget; enable PodLevelResources")
	}
	if len(p.Containers) != len(q.Containers) || len(p.InitContainers) != len(q.InitContainers) ||
		len(q.EphemeralContainers) != 0 || !equality.Semantic.DeepEqual(p.Volumes, q.Volumes) {
		return errors.New("Kubernetes changed Cloud Job containers or private volumes")
	}
	for i := range p.InitContainers {
		if !sameHostContainer(p.InitContainers[i], q.InitContainers[i]) {
			return errors.New("Kubernetes changed a Cloud Job init container")
		}
		defaultProbe(p.InitContainers[i].StartupProbe)
		defaultProbe(q.InitContainers[i].StartupProbe)
	}
	for i := range p.Containers {
		if !sameHostContainer(p.Containers[i], q.Containers[i]) {
			return errors.New("Kubernetes changed a Cloud Job host container")
		}
	}
	// safety: API defaults may fill unset fields, but cannot weaken the authored placement or isolation.
	if !equality.Semantic.DeepDerivative(p, q) {
		return errors.New("Kubernetes changed Cloud Job placement or isolation")
	}
	return nil
}

func sameHostContainer(p, q corev1.Container) bool {
	return slices.Equal(p.Command, q.Command) && slices.Equal(p.Args, q.Args) &&
		equality.Semantic.DeepEqual(p.Env, q.Env) && equality.Semantic.DeepEqual(p.EnvFrom, q.EnvFrom) &&
		equality.Semantic.DeepEqual(p.VolumeMounts, q.VolumeMounts) && equality.Semantic.DeepEqual(p.VolumeDevices, q.VolumeDevices) &&
		equality.Semantic.DeepEqual(p.Resources, q.Resources) && equality.Semantic.DeepEqual(p.Lifecycle, q.Lifecycle)
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
