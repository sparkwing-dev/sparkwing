package launcher

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type syncControl struct {
	*controlFake
	sync func([]CloudJobObservation) ([]CloudJobState, error)
}

func (c syncControl) Sync(_ context.Context, observations []CloudJobObservation) ([]CloudJobState, error) {
	return c.sync(observations)
}

func TestSyncReconcilesAllPersistedJobsTogether(t *testing.T) {
	l, kube, boundary, control := fixture()
	for _, id := range []string{"first", "second"} {
		control.award.Binding.UnitID, control.award.Binding.NodeID = id, id
		if _, err := l.LaunchOne(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	l.Control = syncControl{control, func(observations []CloudJobObservation) ([]CloudJobState, error) {
		calls++
		if len(observations) != 2 {
			t.Fatalf("sync observed %d Jobs, want both persisted Jobs", len(observations))
		}
		states := make([]CloudJobState, len(observations))
		for i, observation := range observations {
			if observation.JobName != jobName(observation.Binding) || observation.NodeUID != "uid" {
				t.Fatal("lost Kubernetes identity")
			}
			states[len(states)-1-i] = CloudJobState{Binding: observation.Binding, LeaseExpiresAt: time.Now().Add(time.Hour), Delete: observation.Binding.UnitID == "first"}
		}
		return states, nil
	}}
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs, err := kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || boundary.releases != 1 || len(jobs.Items) != 1 {
		t.Fatalf("calls=%d releases=%d remaining=%d", calls, boundary.releases, len(jobs.Items))
	}
	binding, _, err := jobOwnership(jobs.Items[0])
	if err != nil || binding.UnitID != "second" {
		t.Fatalf("wrong Job retained: %v", err)
	}
}

func FuzzSyncRejectsUnboundCleanup(f *testing.F) {
	for mode := range uint8(6) {
		f.Add(mode, "replacement", true)
	}
	f.Fuzz(func(t *testing.T, mode uint8, changed string, reverse bool) {
		l, kube, boundary, control := fixture()
		for _, id := range []string{"first", "second"} {
			control.award.Binding.UnitID, control.award.Binding.NodeID = id, id
			if _, err := l.LaunchOne(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		failure := errors.New("controller unavailable")
		l.Control = syncControl{control, func(observations []CloudJobObservation) ([]CloudJobState, error) {
			states := make([]CloudJobState, len(observations))
			for i, observation := range observations {
				states[i] = CloudJobState{Binding: observation.Binding, LeaseExpiresAt: time.Now().Add(time.Hour), Delete: true}
			}
			if reverse {
				slices.Reverse(states)
			}
			switch mode % 6 {
			case 0:
				return nil, failure
			case 1:
				states = states[:len(states)-1]
			case 2:
				states = append(states, states[0])
			case 3:
				states[len(states)-1] = states[0]
			case 4:
				states[len(states)-1].Binding.Holder += changed + "x"
			case 5:
				states[len(states)-1].Binding.UnitID += changed + "x"
			}
			return states, nil
		}}
		if err := l.Sync(t.Context()); err == nil {
			t.Fatal("unbound cleanup accepted")
		}
		jobs, err := kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if boundary.releases != 0 || len(jobs.Items) != 2 {
			t.Fatalf("invalid response performed cleanup: releases=%d remaining=%d", boundary.releases, len(jobs.Items))
		}
	})
}

func TestSyncDefersOneCorruptAwardAndRetiresItsSibling(t *testing.T) {
	l, kube, boundary, control := fixture()
	for _, id := range []string{"broken", "finished"} {
		control.award.Binding.UnitID, control.award.Binding.NodeID = id, id
		if _, err := l.LaunchOne(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	l.Control = syncControl{control, func(observations []CloudJobObservation) ([]CloudJobState, error) {
		states := make([]CloudJobState, 0, len(observations))
		for _, observation := range observations {
			states = append(states, CloudJobState{Binding: observation.Binding, Deferred: observation.Binding.UnitID == "broken", Delete: observation.Binding.UnitID == "finished"})
		}
		return states, nil
	}}
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs, err := kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if boundary.releases != 1 || len(jobs.Items) != 1 {
		t.Fatalf("releases=%d remaining=%d", boundary.releases, len(jobs.Items))
	}
	binding, _, err := jobOwnership(jobs.Items[0])
	if err != nil || binding.UnitID != "broken" {
		t.Fatalf("corrupt award job removed: %v", err)
	}
}
