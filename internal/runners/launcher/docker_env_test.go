package launcher

import (
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func TestCloudWorkloadsUseTheSidecarSocket(t *testing.T) {
	l, _, boundary, control := fixture()
	result, err := BuildJob(t.Context(), time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), l.Config, *control.award, boundary)
	if err != nil {
		t.Fatal(err)
	}
	pod := result.Job.Spec.Template.Spec
	for _, container := range []corev1.Container{pod.InitContainers[1], pod.Containers[0]} {
		if !slices.Contains(container.Env, corev1.EnvVar{Name: "DOCKER_HOST", Value: "unix:///tmp/docker.sock"}) {
			t.Fatalf("%s lacks the Docker sidecar endpoint", container.Name)
		}
		if !slices.Contains(container.VolumeMounts, corev1.VolumeMount{Name: "scratch", MountPath: "/tmp"}) {
			t.Fatalf("%s lacks the socket volume", container.Name)
		}
	}
	if !slices.Contains(pod.InitContainers[0].Args, "--host=unix:///tmp/docker.sock") {
		t.Fatal("Docker sidecar does not listen at the workload endpoint")
	}
}
