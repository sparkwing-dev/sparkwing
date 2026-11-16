package launcher

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func breakerFixture(t *testing.T) (*Launcher, *bytes.Buffer, *int) {
	t.Helper()
	l, kube, boundary, control := fixture()
	boundary.reserveErr = ErrNoDedicatedNode
	var logs bytes.Buffer
	l.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := l.LaunchOne(t.Context()); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatal(err)
	}
	if len(capacityPods(t, kube)) != 1 || len(l.waitingAwards()) != 1 {
		t.Fatal("award is not waiting for capacity")
	}
	control.award = nil
	failures := 0
	l.Control = publicationControl{CloudControl: control, sync: func(observations []CloudJobObservation) ([]CloudJobState, error) {
		states := make([]CloudJobState, len(observations))
		for i, observation := range observations {
			if observation.Capacity != nil && observation.Capacity.Unavailable {
				failures++
				states[i] = CloudJobState{Binding: observation.Binding, Delete: true}
				continue
			}
			states[i] = CloudJobState{Binding: observation.Binding, LeaseExpiresAt: observation.Binding.OwnerDeadline}
		}
		return states, nil
	}}
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	return l, &logs, &failures
}

func TestClosedBreakerFailsWaitingAwardOnce(t *testing.T) {
	l, logs, failures := breakerFixture(t)
	l.Breaker = func(context.Context) (bool, error) { return true, nil }
	for range 2 {
		if _, err := l.LaunchOne(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	pods, err := l.Kube.CoreV1().Pods("cloud").List(t.Context(), metav1.ListOptions{LabelSelector: capacityLabel + "=true"})
	if err != nil || len(pods.Items) != 0 || len(l.waitingAwards()) != 0 || *failures != 1 {
		t.Fatalf("closed breaker: failures=%d pods=%v waiting=%d err=%v", *failures, pods, len(l.waitingAwards()), err)
	}
	if !strings.Contains(logs.String(), "budget breaker is closed") || !strings.Contains(logs.String(), "support_reference=run") || !strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("operator log: %s", logs.String())
	}
}

func TestOpenBreakerKeepsWaiting(t *testing.T) {
	for name, state := range map[string]func(context.Context) (bool, error){
		"open":       func(context.Context) (bool, error) { return false, nil },
		"no breaker": nil,
	} {
		t.Run(name, func(t *testing.T) {
			l, _, failures := breakerFixture(t)
			l.Breaker = state
			if _, err := l.LaunchOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			if *failures != 0 || len(l.waitingAwards()) != 1 {
				t.Fatalf("open breaker failed the award: failures=%d", *failures)
			}
		})
	}
}

func TestUnreadableBreakerWarnsAndKeepsWaiting(t *testing.T) {
	l, logs, failures := breakerFixture(t)
	l.Breaker = func(context.Context) (bool, error) { return false, errors.New("permission denied") }
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	if *failures != 0 || len(l.waitingAwards()) != 1 {
		t.Fatalf("unreadable flag tripped the breaker: failures=%d", *failures)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "budget state unreadable") {
		t.Fatalf("missing warning: %s", logs.String())
	}
}

func TestClosedBreakerLaunchesOnAvailableNode(t *testing.T) {
	l, kube, _, _ := fixture()
	asked := false
	l.Breaker = func(context.Context) (bool, error) {
		asked = true
		return true, nil
	}
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs, err := kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 || asked {
		t.Fatalf("an award with a node consulted the breaker or did not run: jobs=%d asked=%t err=%v", len(jobs.Items), asked, err)
	}
}

func TestClosedBreakerKeepsScheduledCapacity(t *testing.T) {
	l, _, failures := breakerFixture(t)
	pods, err := l.Kube.CoreV1().Pods("cloud").List(t.Context(), metav1.ListOptions{LabelSelector: capacityLabel + "=true"})
	if err != nil {
		t.Fatal(err)
	}
	pod := &pods.Items[0]
	pod.Spec.NodeName = "node"
	if _, err := l.Kube.CoreV1().Pods("cloud").Update(t.Context(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	l.Breaker = func(context.Context) (bool, error) { return true, nil }
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	if *failures != 0 || len(l.waitingAwards()) != 1 {
		t.Fatalf("scheduled demand failed before its node became ready: failures=%d waiting=%d", *failures, len(l.waitingAwards()))
	}
	l.Boundary.(*boundaryFake).reserveErr = nil
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs, err := l.Kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 || *failures != 0 || len(l.waitingAwards()) != 0 {
		t.Fatalf("scheduled capacity did not launch: jobs=%v failures=%d waiting=%d err=%v", jobs, *failures, len(l.waitingAwards()), err)
	}
}
