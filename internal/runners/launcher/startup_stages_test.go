package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/startuptest"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
)

func TestLauncherStartupStagesSurvivePublicationFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "job_create_failure"}[fail], func(t *testing.T) {
			l, kube, _, control := capacityLauncherFixture(t)
			pod, err := capacityPod(l.Config, demandFor(*control.award))
			if err != nil {
				t.Fatal(err)
			}
			pod.UID, pod.Spec.NodeName = "capacity-uid", "node"
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "capacity", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(time.Now().Add(-time.Second))}}}}
			if _, err := kube.CoreV1().Pods(l.Config.Namespace).Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			l.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
			previous := slog.Default()
			slog.SetDefault(l.Logger)
			t.Cleanup(func() { slog.SetDefault(previous) })
			failure := errors.New("private publication failure")
			if fail {
				kube.PrependReactor("create", "jobs", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, failure })
			}
			_, err = l.LaunchOne(t.Context())
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
			want := []string{"launcher_claim_received", "launcher_claim_applied", "launcher_preparation", "launcher_capacity_delete", "launcher_capacity_removal", "launcher_reservation", "launcher_capacity_release", "launcher_job_create"}
			if !fail {
				want = append(want, "launcher_capacity_observed")
			}
			if !fail {
				startuptest.CheckDocumented(t, filepath.Join("..", "..", "..", "docs", "observability.md"), "launcher_", stages)
			}
			if !slices.Equal(stages, want) {
				t.Fatalf("stages=%v want=%v logs=%s", stages, want, logs.String())
			}
			if bytes.Contains(logs.Bytes(), []byte(failure.Error())) {
				t.Fatal("timing includes error text")
			}
		})
	}
}

func TestCapacityAbandonDoesNotRecordStartup(t *testing.T) {
	l, kube, _, control := fixture()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: capacityName(control.award.Binding), Namespace: l.Config.Namespace, UID: "capacity-uid"}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "capacity", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(time.Now().Add(-time.Second))}}}}}}
	if _, err := kube.CoreV1().Pods(l.Config.Namespace).Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	l.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	if err := l.releaseCapacity(t.Context(), control.award.Binding); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Fatalf("abandon recorded startup: %s", logs.String())
	}
}

func TestExpectedStartupWaitsDoNotRecordFailure(t *testing.T) {
	for _, stage := range []string{"preparation", "reservation"} {
		t.Run(stage, func(t *testing.T) {
			l, _, boundary, control := fixture()
			if stage == "preparation" {
				l.Control = preparationControl{CloudControl: control, prepare: func(context.Context, []capability.LeaseRef) (time.Time, error) {
					return time.Time{}, ErrNoDedicatedNode
				}}
			} else {
				boundary.reserveErr = ErrNoDedicatedNode
			}
			var logs bytes.Buffer
			l.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
			if _, err := l.launch(t.Context(), control.award); !errors.Is(err, ErrNoDedicatedNode) {
				t.Fatal(err)
			}
			for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
				if len(line) == 0 {
					continue
				}
				var record map[string]any
				if err := json.Unmarshal(line, &record); err != nil {
					t.Fatal(err)
				}
				if record["success"] == false {
					t.Fatalf("expected wait recorded failure: %s", line)
				}
			}
		})
	}
}

func TestStartupDurationsBracketExchangeAndJobCreation(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	synctest.Test(t, func(t *testing.T) {
		award, unit := bootstrapAward(t, capability.UnitPlan)
		host := BootstrapHost{ControllerURL: "https://controller.example", NewController: func(client.EngineOptions) (controllerwire.Controller, error) {
			return bootstrapController{exchange: func(context.Context) (controllerwire.ExchangeResponse, error) {
				<-time.NewTimer(37 * time.Millisecond).C
				return controllerwire.ExchangeResponse{Claim: unit}, nil
			}}, nil
		}, Host: bootstrapUnitHost(func(context.Context, controllerwire.ClaimedUnit) error { return nil })}
		if err := host.Run(t.Context(), award); err != nil {
			t.Fatal(err)
		}
		l, kube, _, _ := fixture()
		l.Logger = slog.Default()
		kube.PrependReactor("create", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
			<-time.NewTimer(41 * time.Millisecond).C
			return false, nil, nil
		})
		if _, err := l.LaunchOne(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
	durations := map[string]float64{}
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if stage, ok := record["stage"].(string); ok {
			duration, ok := record["duration_ms"].(float64)
			if !ok {
				t.Fatalf("missing duration: %s", line)
			}
			durations[stage] = duration
		}
	}
	for stage, want := range map[string]float64{"bootstrap_exchange": 37, "launcher_job_create": 41} {
		if got := durations[stage]; got != want {
			t.Errorf("%s duration=%v want=%v", stage, got, want)
		}
	}
}
