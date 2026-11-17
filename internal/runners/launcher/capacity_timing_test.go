package launcher

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
)

func startupRecords(t *testing.T, logs *bytes.Buffer) map[string][]map[string]any {
	t.Helper()
	records := map[string][]map[string]any{}
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if stage, ok := record["stage"].(string); ok {
			records[stage] = append(records[stage], record)
		}
	}
	return records
}

func TestConsumedCapacityStartupTiming(t *testing.T) {
	for _, outcome := range []string{"success", "unknown_start", "job_failure", "delete_failure", "wait_failure"} {
		t.Run(outcome, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			synctest.Test(t, func(t *testing.T) {
				l, kube, _, control := capacityLauncherFixture(t)
				l.Logger = slog.Default()
				pod, err := capacityPod(l.Config, demandFor(*control.award))
				if err != nil {
					t.Fatal(err)
				}
				pod.Spec.NodeName, pod.UID = "node", "capacity-uid"
				started := time.Now().Add(-time.Second)
				if outcome != "unknown_start" {
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "capacity", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(started)}}}}
				}
				if err := kube.Tracker().Add(pod); err != nil {
					t.Fatal(err)
				}
				failure := errors.New("private teardown failure")
				deleted := false
				removed := false
				var deleteReturned time.Time
				kube.PrependReactor("delete", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
					deletion := action.(ktesting.DeleteAction)
					if deletion.GetName() != pod.Name {
						return false, nil, nil
					}
					fence := deletion.GetDeleteOptions().Preconditions
					if fence == nil || fence.UID == nil || *fence.UID != pod.UID {
						t.Fatal("capacity deletion lost its UID fence")
					}
					<-time.NewTimer(37 * time.Millisecond).C
					deleted, deleteReturned = true, time.Now()
					if outcome == "delete_failure" {
						return true, nil, failure
					}
					return true, nil, nil
				})
				kube.PrependReactor("get", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
					if !deleted || action.(ktesting.GetAction).GetName() != pod.Name {
						return false, nil, nil
					}
					if outcome == "wait_failure" {
						return true, nil, failure
					}
					if !removed && time.Since(deleteReturned) >= 750*time.Millisecond {
						if err := kube.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), pod.Namespace, pod.Name); err != nil {
							t.Fatal(err)
						}
						removed = true
					}
					return false, nil, nil
				})
				kube.PrependReactor("create", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
					if !deleted || time.Since(deleteReturned) < 750*time.Millisecond {
						t.Fatal("Job published before capacity removal")
					}
					if outcome == "job_failure" {
						return true, nil, failure
					}
					return false, nil, nil
				})
				_, err = l.LaunchOne(t.Context())
				wantFailure := outcome == "job_failure" || outcome == "delete_failure" || outcome == "wait_failure"
				if wantFailure && !errors.Is(err, failure) || !wantFailure && err != nil {
					t.Fatal(err)
				}
				records := startupRecords(t, &logs)
				observed := records["launcher_capacity_observed"]
				if outcome == "success" {
					if len(observed) != 1 || observed[0]["duration_ms"] != float64(1787) {
						t.Fatalf("lost consumed capacity start: %v", observed)
					}
				} else if len(observed) != 0 {
					t.Fatalf("unexpected capacity observation: %v", observed)
				}
				for stage, want := range map[string]float64{"launcher_capacity_delete": 37, "launcher_capacity_removal": 750} {
					got := records[stage]
					if stage == "launcher_capacity_removal" && outcome == "delete_failure" {
						if len(got) != 0 {
							t.Fatalf("removal ran after failed deletion: %v", got)
						}
						continue
					}
					failed := stage == "launcher_capacity_delete" && outcome == "delete_failure" || stage == "launcher_capacity_removal" && outcome == "wait_failure"
					if failed && stage == "launcher_capacity_removal" {
						want = 0
					}
					if len(got) != 1 || got[0]["duration_ms"] != want || got[0]["success"] != !failed {
						t.Errorf("%s=%v want duration=%v success=%v", stage, got, want, !failed)
					}
				}
				if bytes.Contains(logs.Bytes(), control.award.Bootstrap) || bytes.Contains(logs.Bytes(), []byte(failure.Error())) {
					t.Fatal("startup logs contain private data")
				}
				jobs, err := kube.Tracker().List(batchv1.SchemeGroupVersion.WithResource("jobs"), batchv1.SchemeGroupVersion.WithKind("Job"), l.Config.Namespace)
				if err != nil || wantFailure && len(jobs.(*batchv1.JobList).Items) != 0 {
					t.Fatalf("failed teardown/publication left Job: %v %v", jobs, err)
				}
				if outcome == "job_failure" {
					node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
					if err != nil || reservationEvidence(node) {
						t.Fatalf("failed publication retained its consumed-capacity reservation: %v %v", node, err)
					}
				}
			})
		})
	}
}

func TestConsumedCapacityTimingPreservesReservationFence(t *testing.T) {
	for _, retire := range []bool{false, true} {
		t.Run(map[bool]string{false: "release", true: "retire"}[retire], func(t *testing.T) {
			l, kube, boundary, control := capacityLauncherFixture(t)
			pod, err := capacityPod(l.Config, demandFor(*control.award))
			if err != nil {
				t.Fatal(err)
			}
			pod.Spec.NodeName, pod.UID = "node", "capacity-uid"
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "capacity", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()}}}}
			if err := kube.Tracker().Add(pod); err != nil {
				t.Fatal(err)
			}
			r, err := boundary.Reserve(t.Context(), control.award.Binding, jobName(control.award.Binding), CloudPlacement{RequestedClass: "2"})
			if err != nil {
				t.Fatal(err)
			}
			changed := r
			changed.Award.Generation++
			finish := boundary.Release
			if retire {
				finish = boundary.Retire
			}
			if err := finish(t.Context(), changed); err == nil {
				t.Fatal("changed award fence permitted teardown")
			}
			if err := finish(t.Context(), r); err != nil {
				t.Fatalf("consumed-capacity timestamp prevented fenced teardown: %v", err)
			}
		})
	}
}
