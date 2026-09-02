package pool

import (
	"context"
	"fmt"
	"io"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// WarmerServiceAccountName is the ServiceAccount the warmer pod runs as when
// the caller names none. It carries no RBAC rules; the sparkwing-full chart
// creates a release-scoped account instead and passes its name to the
// controller.
const WarmerServiceAccountName = "sparkwing-cache-warmer"

// safety: the warmer runs privileged, so every entry must parse as a registry reference before it reaches the pod
var imageReferencePattern = regexp.MustCompile(`^(?:(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?)*|\[[0-9a-fA-F:.]+\])(?::[0-9]{1,5})?/)?` +
	`[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*` +
	`(?::[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127})?(?:@sha256:[a-f0-9]{64})?$`)

// safety: the list arrives as positional arguments so no entry is ever part of the script text
const warmerScript = `set -e
echo 'starting dockerd...'
dockerd --host=unix:///var/run/docker.sock --data-root=/var/lib/docker &
DOCKERD_PID=$!
echo 'waiting for docker daemon...'
until docker info > /dev/null 2>&1; do sleep 1; done
echo 'docker ready'
for img in "$@"; do
	echo "==> pull $img"
	docker pull "$img" || echo "WARN: failed to pull $img"
done
echo 'warmer complete'
kill $DOCKERD_PID
wait $DOCKERD_PID 2>/dev/null || true
`

// safety: a ConfigMap writer controls the list length, so only the first entries are read and rejections are summarized
const (
	maxWarmImages         = 64
	maxNamedRejections    = 3
	maxRejectionNameChars = 100
)

var (
	warmImageLogMu       sync.Mutex
	lastWarmImageWarning string
)

func acceptedWarmImages(images []string) []string {
	considered := images
	unread := 0
	if len(considered) > maxWarmImages {
		unread = len(considered) - maxWarmImages
		considered = considered[:maxWarmImages]
	}

	accepted := make([]string, 0, len(considered))
	var rejected []string
	for _, img := range considered {
		if !imageReferencePattern.MatchString(img) {
			rejected = append(rejected, img)
			continue
		}
		accepted = append(accepted, img)
	}

	logWarmImageRejections(len(images), rejected, unread)
	if len(accepted) == 0 {
		return nil
	}
	return accepted
}

func logWarmImageRejections(total int, rejected []string, unread int) {
	line := ""
	if len(rejected) > 0 || unread > 0 {
		var msg strings.Builder
		fmt.Fprintf(&msg, "warmer: rejected %d of %d warm images", len(rejected)+unread, total)
		if len(rejected) > 0 {
			named := rejected
			if len(named) > maxNamedRejections {
				named = named[:maxNamedRejections]
			}
			quoted := make([]string, 0, len(named))
			for _, img := range named {
				quoted = append(quoted, strconv.Quote(truncateForLog(img)))
			}
			fmt.Fprintf(&msg, ": %s", strings.Join(quoted, ", "))
			if more := len(rejected) - len(named); more > 0 {
				fmt.Fprintf(&msg, " and %d more", more)
			}
		}
		if unread > 0 {
			fmt.Fprintf(&msg, "; %d entries past the %d entry limit were not read", unread, maxWarmImages)
		}
		line = msg.String()
	}

	warmImageLogMu.Lock()
	defer warmImageLogMu.Unlock()
	if line == lastWarmImageWarning {
		return
	}
	lastWarmImageWarning = line
	if line != "" {
		log.Print(line)
	}
}

func truncateForLog(img string) string {
	if len(img) <= maxRejectionNameChars {
		return img
	}
	return img[:maxRejectionNameChars] + "..."
}

// WarmPVC runs a short-lived DinD pod that mounts the target PVC at /var/lib/docker
// and pulls the warm image list into it. Once the pod completes, the PVC contains
// a pre-populated Docker storage directory. At most 64 entries are read, and
// those that do not parse as image references are dropped rather than passed to
// the pod; one summary line per read reports what was dropped. An empty
// serviceAccount falls back to [WarmerServiceAccountName].
func WarmPVC(ctx context.Context, client kubernetes.Interface, namespace, pvcName, serviceAccount string, warmImages []string) error {
	if serviceAccount == "" {
		serviceAccount = WarmerServiceAccountName
	}
	podName := fmt.Sprintf("sparkwing-cache-warmer-%s-%d", strings.TrimPrefix(pvcName, "sparkwing-cache-pool-"), time.Now().Unix())

	images := acceptedWarmImages(warmImages)

	privileged := true
	automount := false
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
			// safety: the warmer runs privileged, so it carries no API token
			ServiceAccountName:           serviceAccount,
			AutomountServiceAccountToken: &automount,
			Containers: []corev1.Container{
				{
					Name:    "warmer",
					Image:   "docker:27-dind@sha256:f649ef046008ca7f926a2571c32b0ac22e5c59eb61b959617f9acc2a4c638cf5",
					Command: []string{"sh", "-c", warmerScript, "warmer"},
					Args:    images,
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

	timeout := 30 * time.Minute
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Second)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = client.CoreV1().Pods(namespace).Delete(cleanupCtx, podName, metav1.DeleteOptions{})
			cancel()
			return fmt.Errorf("waiting for warmer pod %s: %w", podName, ctx.Err())
		case <-deadline.C:
			_ = client.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
			return fmt.Errorf("warmer pod %s timed out after %s", podName, timeout)
		case <-poll.C:
		}
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
