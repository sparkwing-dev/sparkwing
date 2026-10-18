package launcher

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const podENI corev1.ResourceName = "vpc.amazonaws.com/pod-eni"

// safety: only this error rejects an award; API failures retry on the next sync.
// An unadmittable Job never ran a Pod, so its rejection leaves the node in service.
type podMismatch struct {
	message      string
	unadmittable bool
}

func (e *podMismatch) Error() string { return e.message }

// safety: admission enforces Pod shape in the job namespace; the policy cannot know
// an award's team, node, images or size, so these fields are compared here.
func awardPod(want, got *corev1.PodTemplateSpec) error {
	if got.Labels[TeamLabel] != want.Labels[TeamLabel] || got.Labels[JobLabel] != want.Labels[JobLabel] || !maps.Equal(got.Spec.NodeSelector, want.Spec.NodeSelector) || want.Spec.NodeName != "" && got.Spec.NodeName != want.Spec.NodeName {
		return errors.New("placement differs from the award")
	}
	if !equality.Semantic.DeepEqual(got.Spec.Resources, want.Spec.Resources) {
		return errors.New("Pod resources differ from the award")
	}
	a, b := slices.Concat(want.Spec.InitContainers, want.Spec.Containers), slices.Concat(got.Spec.InitContainers, got.Spec.Containers)
	if len(a) != len(b) {
		return errors.New("container set differs from the award")
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Image != b[i].Image || !equality.Semantic.DeepEqual(withoutENI(a[i].Resources), withoutENI(b[i].Resources)) {
			return fmt.Errorf("container %s differs from the award", a[i].Name)
		}
	}
	return nil
}

func withoutENI(r corev1.ResourceRequirements) corev1.ResourceRequirements {
	strip := func(l corev1.ResourceList) corev1.ResourceList {
		l = maps.Clone(l)
		delete(l, podENI)
		if len(l) == 0 {
			return nil
		}
		return l
	}
	r.Requests, r.Limits = strip(r.Requests), strip(r.Limits)
	return r
}

func (l *Launcher) checkJob(ctx context.Context, job *batchv1.Job, binding CloudJobBinding, r CloudNodeReservation) error {
	template := &job.Spec.Template
	if template.Spec.Resources == nil {
		return &podMismatch{message: fmt.Sprintf("Cloud Job %s has no awarded resources", job.Name)}
	}
	// safety: create-time checks pinned the template to the award and admission keeps it unchanged,
	// so a chart upgrade's new digests only matter for Pods admission will now refuse.
	cfg := l.Config
	cfg.RunnerImage, cfg.DockerImage = templateImage(template, "run-node"), templateImage(template, "docker")
	current := cfg.RunnerImage == l.Config.RunnerImage && cfg.DockerImage == l.Config.DockerImage
	want := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{TeamLabel: labelHash(binding.Team), JobLabel: job.Name}}, Spec: cloudPod(cfg, CloudJobAward{Binding: binding}, r, job.Name, template.Spec.Resources.Requests)}
	if err := awardPod(&want, template); err != nil {
		return &podMismatch{message: fmt.Sprintf("Cloud Job %s: %v", job.Name, err)}
	}
	pods, err := l.Kube.CoreV1().Pods(l.Config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: JobLabel + "=" + job.Name})
	if err != nil {
		return err
	}
	owned := 0
	for i := range pods.Items {
		pod := &pods.Items[i]
		if owner := metav1.GetControllerOf(pod); owner == nil || owner.UID != job.UID {
			continue
		}
		owned++
		if err := awardPod(template, &corev1.PodTemplateSpec{ObjectMeta: pod.ObjectMeta, Spec: pod.Spec}); err != nil {
			return &podMismatch{message: fmt.Sprintf("Cloud Pod %s: %v", pod.Name, err)}
		}
	}
	if !current && owned == 0 && (job.Spec.Suspend == nil || !*job.Spec.Suspend) {
		return &podMismatch{message: fmt.Sprintf("Cloud Job %s pins images admission no longer allows", job.Name), unadmittable: true}
	}
	return nil
}

func templateImage(t *corev1.PodTemplateSpec, name string) string {
	for _, c := range slices.Concat(t.Spec.InitContainers, t.Spec.Containers) {
		if c.Name == name {
			return c.Image
		}
	}
	return ""
}

func (l *Launcher) enforceAward(ctx context.Context, job *batchv1.Job, binding CloudJobBinding, r CloudNodeReservation) error {
	err := l.checkJob(ctx, job, binding, r)
	var mismatch *podMismatch
	if !errors.As(err, &mismatch) {
		return err
	}
	reason := rejectedPod
	if mismatch.unadmittable {
		reason = rejectedUnadmittable
	}
	return errors.Join(err, l.rejectJob(ctx, job, binding, r, reason))
}
