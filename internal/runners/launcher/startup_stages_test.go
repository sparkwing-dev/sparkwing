package launcher

import (
	"bytes"
	"encoding/json"
	"errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"log/slog"
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
)

func TestLauncherStartupStagesSurvivePublicationFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "job_create_failure"}[fail], func(t *testing.T) {
			l, kube, _, control := fixture()
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: capacityName(control.award.Binding), Namespace: l.Config.Namespace, UID: "capacity-uid"}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "capacity", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(time.Now().Add(-time.Second))}}}}}}
			if _, err := kube.CoreV1().Pods(l.Config.Namespace).Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			l.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
			failure := errors.New("private publication failure")
			if fail {
				kube.PrependReactor("create", "jobs", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, failure })
			}
			_, err := l.LaunchOne(t.Context())
			if fail && !errors.Is(err, failure) || !fail && err != nil {
				t.Fatal(err)
			}
			var stages []string
			for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
				if len(line) == 0 {
					continue
				}
				var record map[string]any
				if err := json.Unmarshal(line, &record); err != nil {
					t.Fatal(err)
				}
				stage, ok := record["stage"].(string)
				if !ok {
					continue
				}
				stages = append(stages, stage)
				if duration, ok := record["duration_ms"].(float64); !ok || duration < 0 {
					t.Fatalf("missing duration: %s", line)
				}
				if record["success"] != (!fail || stage != "launcher_job_create") {
					t.Fatalf("wrong result: %s", line)
				}
			}
			want := []string{"launcher_preparation", "launcher_reservation", "launcher_capacity_observed", "launcher_capacity_release", "launcher_job_create"}
			if !slices.Equal(stages, want) {
				t.Fatalf("stages=%v want=%v logs=%s", stages, want, logs.String())
			}
			if bytes.Contains(logs.Bytes(), []byte(failure.Error())) {
				t.Fatal("timing includes error text")
			}
		})
	}
}
