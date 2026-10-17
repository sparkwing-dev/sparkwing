package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

const capacityLabel = "sparkwing.dev/capacity-request"

func teamTolerations(team string) []corev1.Toleration {
	return []corev1.Toleration{{Key: "sparkwing.dev/cpu-band", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}, {Key: TeamNodeLabel, Value: labelHash(team), Operator: corev1.TolerationOpEqual, Effect: corev1.TaintEffectNoSchedule}}
}

// safety: the pod pends until Karpenter adds a node from the team's own pool,
// and carries nothing from the award beyond its size and deadline.
func capacityPod(cfg CloudJobConfig, award CloudJobAward, now time.Time) (*corev1.Pod, error) {
	budget, err := cloudBudget(cfg, award)
	if err != nil {
		return nil, err
	}
	deadline, err := cloudDeadline(now, award.Binding)
	if err != nil {
		return nil, err
	}
	b := award.Binding
	sum := sha256.Sum256(fmt.Appendf(nil, "%q/%q/%q/%q/%q", b.Team, b.RunID, b.NodeID, b.Kind, b.UnitID))
	requests := budget.DeepCopy()
	requests[corev1.ResourceEphemeralStorage] = *resource.NewQuantity(limits.JobScratchBytes, resource.BinarySI)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sw-capacity-" + hex.EncodeToString(sum[:24]), Namespace: cfg.Namespace,
			Labels:      map[string]string{"app": "sparkwing-job", "app.kubernetes.io/managed-by": managedByLauncher, capacityLabel: "true", TeamLabel: labelHash(b.Team)},
			Annotations: map[string]string{deadlineAnnotation: deadline.Format(time.RFC3339Nano)},
		},
		Spec: corev1.PodSpec{
			Tolerations: teamTolerations(b.Team), NodeSelector: map[string]string{TeamNodeLabel: labelHash(b.Team)},
			ServiceAccountName: ServiceAccount, AutomountServiceAccountToken: ptr(false), EnableServiceLinks: ptr(false), RestartPolicy: corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr(true), RunAsUser: ptr(int64(65534)), RunAsGroup: ptr(int64(65534)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{
				Name: "capacity", Image: cfg.RunnerImage, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{JobBinary}, Args: []string{"version"},
				Resources:       corev1.ResourceRequirements{Requests: requests, Limits: requests.DeepCopy()},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), ReadOnlyRootFilesystem: ptr(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			}},
		},
	}, nil
}

func (l *Launcher) requestCapacity(ctx context.Context, award CloudJobAward) error {
	pod, err := capacityPod(l.Config, award, time.Now())
	if err != nil {
		return err
	}
	_, err = l.Kube.CoreV1().Pods(l.Config.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (l *Launcher) syncCapacity(ctx context.Context) error {
	pods, err := l.Kube.CoreV1().Pods(l.Config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: capacityLabel + "=true"})
	if err != nil {
		return err
	}
	var errs []error
	for _, pod := range pods.Items {
		served, err := l.capacityServed(ctx, pod)
		if err == nil && served {
			err = l.Kube.CoreV1().Pods(l.Config.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID}})
		}
		if err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// safety: a request outlives node registration, so a new node is never left
// empty, and open to consolidation, before it is Ready.
func (l *Launcher) capacityServed(ctx context.Context, pod corev1.Pod) (bool, error) {
	deadline, err := time.Parse(time.RFC3339Nano, pod.Annotations[deadlineAnnotation])
	if err != nil || !deadline.After(time.Now()) {
		return true, nil
	}
	if pod.Spec.NodeName == "" {
		return false, nil
	}
	node, err := l.Kube.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return nodeReady(*node), nil
}
