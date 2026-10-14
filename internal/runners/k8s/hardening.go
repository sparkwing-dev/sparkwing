package k8s

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// DefaultScratchLimit bounds the scratch volume of a pod that runs pipeline
// code when its operator sets no other limit.
const DefaultScratchLimit = "20Gi"

const scratchVolumeName = "scratch"

// HardenedPodSpec returns the pod settings every pod that runs pipeline code
// shares; the caller adds its containers, identity and placement. Each
// container must pass through [HardenedContainer].
func HardenedPodSpec(scratchLimit resource.Quantity) corev1.PodSpec {
	return corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever,
		// safety: pipeline code runs here, so the pod gets no API token.
		AutomountServiceAccountToken: boolPtr(false),
		// safety: service links would put every Service in the namespace into
		// the pipeline's environment.
		EnableServiceLinks: boolPtr(false),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   boolPtr(true),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		// safety: a size limit makes the kubelet evict a pod that fills its
		// scratch instead of letting it fill the node's disk.
		Volumes: []corev1.Volume{{
			Name:         scratchVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &scratchLimit}},
		}},
	}
}

// HardenedContainer locks down a container of a [HardenedPodSpec] pod and
// mounts its scratch volume at /tmp.
func HardenedContainer(c corev1.Container) corev1.Container {
	c.SecurityContext = &corev1.SecurityContext{
		AllowPrivilegeEscalation: boolPtr(false),
		ReadOnlyRootFilesystem:   boolPtr(true),
		RunAsNonRoot:             boolPtr(true),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	// safety: the read-only root leaves no other writable path, and HOME, the
	// caches and SPARKWING_HOME all live here.
	c.VolumeMounts = []corev1.VolumeMount{{Name: scratchVolumeName, MountPath: "/tmp"}}
	return c
}
