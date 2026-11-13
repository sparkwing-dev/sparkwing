package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
)

type startupControl struct {
	*controlFake
	observed    []CloudJobObservation
	failStartup bool
}

func (c *startupControl) Sync(ctx context.Context, observations []CloudJobObservation) ([]CloudJobState, error) {
	c.observed = append(c.observed, observations...)
	if c.failStartup {
		for _, o := range observations {
			if o.Startup != nil {
				return nil, errors.New("timing sink unavailable")
			}
		}
	}
	return c.controlFake.Sync(ctx, observations)
}

func TestLauncherReportsOwnedPodStartup(t *testing.T) {
	l, kube, _, base := fixture()
	control := &startupControl{controlFake: base}
	l.Control = control
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs, err := kube.BatchV1().Jobs(l.Config.Namespace).List(t.Context(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 {
		t.Fatalf("jobs=%+v %v", jobs, err)
	}
	job := jobs.Items[0]
	pod := &corev1.Pod{ObjectMeta: *job.Spec.Template.ObjectMeta.DeepCopy(), Spec: *job.Spec.Template.Spec.DeepCopy()}
	pod.Name, pod.Namespace, pod.UID = job.Name+"-pod", job.Namespace, "owned-pod"
	pod.OwnerReferences = []metav1.OwnerReference{{Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr(true)}}
	scheduled := time.Now().UTC().Add(-3 * time.Second).Truncate(time.Second)
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(scheduled)}}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "run-node", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(scheduled.Add(2 * time.Second))}}}}
	if _, err := kube.CoreV1().Pods(job.Namespace).Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{"owned-pod", "foreign-pod"} {
		event := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: uid, Namespace: job.Namespace}, InvolvedObject: corev1.ObjectReference{Kind: "Pod", UID: pod.UID, FieldPath: "spec.containers{run-node}"}, Reason: "Pulled", LastTimestamp: metav1.NewTime(scheduled.Add(time.Second))}
		if uid == "foreign-pod" {
			event.InvolvedObject.UID = "foreign-pod"
			event.LastTimestamp = metav1.NewTime(scheduled.Add(-time.Hour))
		}
		if _, err := kube.CoreV1().Events(job.Namespace).Create(t.Context(), event, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(control.observed)
	if err != nil {
		t.Fatal(err)
	}
	var observations []struct {
		Unheld  bool
		Startup *struct {
			PodUID             string     `json:"pod_uid"`
			ScheduledAt        *time.Time `json:"scheduled_at"`
			ImagePulledAt      *time.Time `json:"image_pulled_at"`
			ContainerStartedAt *time.Time `json:"container_started_at"`
		}
	}
	if err := json.Unmarshal(body, &observations); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range observations {
		if o.Startup == nil {
			continue
		}
		found = true
		if !o.Unheld {
			t.Fatal("startup diagnostics must not renew preparation")
		}
		if o.Startup.PodUID != "owned-pod" || o.Startup.ScheduledAt == nil || !o.Startup.ScheduledAt.Equal(scheduled) || o.Startup.ImagePulledAt == nil || !o.Startup.ImagePulledAt.Equal(scheduled.Add(time.Second)) || o.Startup.ContainerStartedAt == nil || !o.Startup.ContainerStartedAt.Equal(scheduled.Add(2*time.Second)) {
			t.Fatalf("startup=%+v", o.Startup)
		}
	}
	if !found {
		t.Fatalf("owned pod startup missing: %s", body)
	}
}

func TestLauncherStopsProbingAStartedPreboundPod(t *testing.T) {
	l, kube, _, _ := fixture()
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	pods, err := kube.CoreV1().Pods(l.Config.Namespace).List(t.Context(), metav1.ListOptions{})
	if err != nil || len(pods.Items) != 1 {
		t.Fatalf("pods=%+v %v", pods, err)
	}
	pod := pods.Items[0].DeepCopy()
	now := metav1.Now()
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "run-node", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: now}}}}
	if err := kube.Tracker().Add(pod); err != nil {
		t.Fatal(err)
	}
	event := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "pulled", Namespace: pod.Namespace}, InvolvedObject: corev1.ObjectReference{Kind: "Pod", UID: pod.UID, FieldPath: "spec.containers{run-node}"}, Reason: "Pulled", FirstTimestamp: now}
	if _, err := kube.CoreV1().Events(pod.Namespace).Create(t.Context(), event, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		n := 0
		for _, a := range kube.Actions() {
			if a.GetVerb() == "list" && a.GetResource().Resource == "events" {
				n++
			}
		}
		return n
	}
	before := count()
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if before != 1 || count() != before {
		t.Fatalf("started prebound Pod keeps polling image events: before=%d after=%d", before, count())
	}
}

func TestLauncherActivationUsesJobStartTime(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(map[bool]string{true: "observed", false: "unknown"}[known], func(t *testing.T) {
			l, kube, _, base := fixture()
			control := &startupControl{controlFake: base}
			l.Control = control
			if _, err := l.LaunchOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			jobs, err := kube.BatchV1().Jobs(l.Config.Namespace).List(t.Context(), metav1.ListOptions{})
			if err != nil || len(jobs.Items) != 1 {
				t.Fatalf("jobs=%+v err=%v", jobs, err)
			}
			job := jobs.Items[0].DeepCopy()
			request := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
			actual := metav1.NewTime(request.Add(30 * time.Second))
			job.Annotations[launchAnnotation] = request.Format(time.RFC3339Nano)
			if known {
				job.Status.StartTime = &actual
			}
			if _, err := kube.BatchV1().Jobs(job.Namespace).Update(t.Context(), job, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			pods, err := kube.CoreV1().Pods(job.Namespace).List(t.Context(), metav1.ListOptions{})
			if err != nil || len(pods.Items) != 1 {
				t.Fatalf("pods=%+v err=%v", pods, err)
			}
			pod := pods.Items[0].DeepCopy()
			pod.CreationTimestamp = metav1.NewTime(request.Add(31 * time.Second))
			if err := kube.Tracker().Add(pod); err != nil {
				t.Fatal(err)
			}
			control.observed = nil
			if err := l.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, o := range control.observed {
				if o.Startup != nil {
					if known && (o.Startup.JobActivatedAt == nil || !o.Startup.JobActivatedAt.Equal(actual.Time)) {
						t.Fatalf("activation=%v, want actual Job start %v", o.Startup.JobActivatedAt, actual.Time)
					}
					if !known && o.Startup.JobActivatedAt != nil {
						t.Fatalf("invented activation=%v from request annotation", o.Startup.JobActivatedAt)
					}
					return
				}
			}
			t.Fatal("no startup observation")
		})
	}
}

func TestLauncherBoundsMissingImageEventProbes(t *testing.T) {
	for _, failure := range []string{"missing", "events unavailable", "receipt unavailable"} {
		t.Run(failure, func(t *testing.T) {
			l, kube, _, base := fixture()
			control := &startupControl{controlFake: base}
			l.Control = control
			if _, err := l.LaunchOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			control.failStartup = failure == "receipt unavailable"
			if failure == "events unavailable" {
				kube.PrependReactor("list", "events", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("events unavailable")
				})
			}
			pods, err := kube.CoreV1().Pods(l.Config.Namespace).List(t.Context(), metav1.ListOptions{})
			if err != nil || len(pods.Items) != 1 {
				t.Fatalf("pods=%+v err=%v", pods, err)
			}
			pod := pods.Items[0].DeepCopy()
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "run-node", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()}}}}
			if err := kube.Tracker().Add(pod); err != nil {
				t.Fatal(err)
			}
			for range 12 {
				if err := l.Sync(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			polls := 0
			for _, a := range kube.Actions() {
				if a.GetVerb() == "list" && a.GetResource().Resource == "events" {
					polls++
				}
			}
			if polls != 3 {
				t.Fatalf("running Pod image polls=%d, want 3 across 12 reconciliations", polls)
			}
			found := false
			for _, o := range control.observed {
				if o.Startup != nil {
					found = true
					if o.Startup.ContainerStartedAt == nil || o.Startup.ScheduledAt != nil || o.Startup.ImagePulledAt != nil {
						t.Fatalf("startup=%+v", o.Startup)
					}
				}
			}
			if !found {
				t.Fatal("container startup was not reported")
			}
		})
	}
}
