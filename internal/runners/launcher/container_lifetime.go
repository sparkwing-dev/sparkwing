package launcher

import corev1 "k8s.io/api/core/v1"

const (
	lifetimeAnnotation = "sparkwing.dev/funded-unix"
	lifetimePath       = "/etc/sparkwing/lifetime/deadline"
	lifetimeVariable   = "SPARKWING_FUNDED_DEADLINE"
)

// The read-only downward projection carries only launcher-acknowledged renewals.
// A delayed projection can stop work early, but cannot add unfunded time.
const containerLifetime = `
deadline="$SPARKWING_FUNDED_DEADLINE"
stop() {
  kill -KILL -1
  exit 124
}
bounded() {
  if [ -r "` + lifetimePath + `" ]; then deadline=$(cat "` + lifetimePath + `") || return 1; fi
  case "$deadline" in ''|*[!0-9]*) return 1;; esac
  [ "$(date +%s)" -lt "$deadline" ]
}
bounded || exit 124
if [ "$1" = dockerd-entrypoint.sh ] && { [ -e "` + DockerStopPath + `" ] || [ -L "` + DockerStopPath + `" ]; }; then exit 124; fi
trap stop TERM INT
"$@" &
child=$!
while kill -0 "$child" 2>/dev/null; do
  bounded || stop
  if [ "$1" = dockerd-entrypoint.sh ] && { [ -e "` + DockerStopPath + `" ] || [ -L "` + DockerStopPath + `" ]; }; then stop; fi
  sleep 0.1
done
wait "$child"
`

func lifetimeCommand(entrypoint string) []string {
	return []string{"sh", "-c", containerLifetime, "sparkwing-lifetime", entrypoint}
}

func lifetimeField() *corev1.ObjectFieldSelector {
	return &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.annotations['" + lifetimeAnnotation + "']"}
}

func lifetimeEnv() corev1.EnvVar {
	return corev1.EnvVar{Name: lifetimeVariable, ValueFrom: &corev1.EnvVarSource{FieldRef: lifetimeField()}}
}

func lifetimeVolume() corev1.Volume {
	return corev1.Volume{Name: "lifetime", VolumeSource: corev1.VolumeSource{DownwardAPI: &corev1.DownwardAPIVolumeSource{DefaultMode: ptr(int32(0o444)), Items: []corev1.DownwardAPIVolumeFile{{Path: "deadline", FieldRef: lifetimeField()}}}}}
}

func lifetimeMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: "lifetime", MountPath: "/etc/sparkwing/lifetime", ReadOnly: true}
}
