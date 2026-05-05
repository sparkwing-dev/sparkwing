package pool

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// WarmPVC runs a short-lived DinD pod that mounts the target PVC at /var/lib/docker
// and pulls the warm image list into it. Once the pod completes, the PVC contains
// a pre-populated Docker storage directory.
func WarmPVC(ctx context.Context, client kubernetes.Interface, namespace, pvcName string, warmImages []string) error {
	podName := fmt.Sprintf("sparkwing-cache-warmer-%s-%d", strings.TrimPrefix(pvcName, "sparkwing-cache-pool-"), time.Now().Unix())

	// Build the pull script that runs inside the DinD container
	var script strings.Builder
	script.WriteString("set -e\n")
	script.WriteString("echo 'starting dockerd...'\n")
	script.WriteString("dockerd --host=unix:///var/run/docker.sock --data-root=/var/lib/docker &\n")
	script.WriteString("DOCKERD_PID=$!\n")
	script.WriteString("echo 'waiting for docker daemon...'\n")
	script.WriteString("until docker info > /dev/null 2>&1; do sleep 1; done\n")
	script.WriteString("echo 'docker ready'\n")
	for _, img := range warmImages {
		fmt.Fprintf(&script, "echo '==> pull %s'; docker pull %s || echo 'WARN: failed to pull %s'\n", img, img, img)
	}
	script.WriteString("echo 'warmer complete'\n")
	script.WriteString("kill $DOCKERD_PID\n")
	script.WriteString("wait $DOCKERD_PID 2>/dev/null || true\n")

	privileged := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			Labels: map[string]string{
				"app":                   "sparkwing-cache-warmer",
				"sparkwing.dev/managed": "pool-manager",
				"sparkwing.dev/pvc":     pvcName,
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:  "warmer",
					Image: "docker:27-dind@sha256:f649ef046008ca7f926a2571c32b0ac22e5c59eb61b959617f9acc2a4c638cf5",
					Command: []string{
						"sh", "-c", script.String(),
					},
					SecurityContext: &corev1.SecurityContext{
						Privileged: &privileged,
					},
					Env: []corev1.EnvVar{
						{Name: "DOCKER_TLS_CERTDIR", Value: ""},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "cache",
							MountPath: "/var/lib/docker",
						},
					},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("512Mi"),
						},
					},
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							Exec: &corev1.ExecAction{
								Command: []string{"sh", "-c", "docker info > /dev/null 2>&1"},
							},
						},
						InitialDelaySeconds: 2,
						PeriodSeconds:       2,
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "cache",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvcName,
						},
					},
				},
			},
		},
	}

	created, err := client.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating warmer pod: %w", err)
	}
	log.Printf("warmer: warming %s via pod %s", pvcName, created.Name)

	// Wait for completion
	timeout := 30 * time.Minute
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		p, err := client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			log.Printf("warmer: warning: polling warmer pod: %v", err)
			continue
		}
		switch p.Status.Phase {
		case corev1.PodSucceeded:
			log.Printf("warmer: pod %s completed successfully", podName)
			_ = client.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
			return nil
		case corev1.PodFailed:
			logs := FetchPodLogs(ctx, client, namespace, podName)
			_ = client.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
			return fmt.Errorf("warmer pod failed:\n%s", logs)
		}
	}

	_ = client.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
	return fmt.Errorf("warmer pod %s timed out after %s", podName, timeout)
}

// FetchPodLogs retrieves logs from a pod (used by warmer for error reporting).
func FetchPodLogs(ctx context.Context, client kubernetes.Interface, namespace, name string) string {
	req := client.CoreV1().Pods(namespace).GetLogs(name, &corev1.PodLogOptions{})
	stream, err := req.Stream(ctx)
	if err != nil {
		return fmt.Sprintf("(could not fetch logs: %v)", err)
	}
	defer stream.Close()
	data, _ := io.ReadAll(stream)
	return strings.TrimSpace(string(data))
}
