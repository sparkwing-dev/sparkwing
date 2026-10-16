package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func FuzzControllerReservationRetirementMatchesObservation(f *testing.F) {
	for mode := uint8(0); mode < 11; mode++ {
		for kind := uint8(0); kind < 3; kind++ {
			f.Add(mode, kind, []byte("replacement"))
		}
	}
	f.Fuzz(func(t *testing.T, mode, kind uint8, input []byte) {
		mode %= 11
		unit := wireAward([]capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}[kind%3])
		unit.Capability = ""
		binding, err := cloudBinding(unit, capability.DeliveryBootstrap)
		if err != nil {
			t.Fatal(err)
		}
		observation := CloudJobObservation{Binding: binding, JobName: jobName(binding), NodeUID: "node-uid", ReservationID: "reservation"}
		ref := controllerwire.LauncherReservationRef{Award: binding.LeaseRef(), NodeUID: observation.NodeUID, ReservationID: observation.ReservationID}
		sum := sha256.Sum256(input)
		foreign := "foreign-" + hex.EncodeToString(sum[:8])
		control, err := NewControllerControl(launcherWireFake{sync: func(_ context.Context, request controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
			want := controllerwire.LauncherObservation{Award: ref.Award, NodeUID: ref.NodeUID, ReservationID: ref.ReservationID}
			if len(request.Observed) != 1 || request.Observed[0] != want {
				t.Fatalf("reservation observation gained a Job or lost its fence: %+v", request)
			}
			switch mode {
			case 1:
				ref.Award.Holder = foreign
			case 2:
				ref.Award.Generation++
			case 3:
				ref.Award.ID = foreign
			case 4:
				ref.Award.Kind = capability.UnitTrigger
			case 5:
				ref.NodeUID = foreign
			case 6:
				ref.ReservationID = foreign
			case 7:
				return controllerwire.LauncherSyncResponse{Remove: []controllerwire.LauncherJobRef{{Namespace: "cloud", Name: observation.JobName, UID: "invented-job"}}}, nil
			case 8:
				return controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{unit}}, nil
			case 9:
				return controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{unit}, RemoveReservations: []controllerwire.LauncherReservationRef{ref}}, nil
			case 10:
				return controllerwire.LauncherSyncResponse{RemoveReservations: []controllerwire.LauncherReservationRef{ref}}, errors.New("uncertain response")
			}
			return controllerwire.LauncherSyncResponse{RemoveReservations: []controllerwire.LauncherReservationRef{ref}}, nil
		}}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{unit.Unit}})
		if err != nil {
			t.Fatal(err)
		}
		states, err := control.Sync(t.Context(), []CloudJobObservation{observation})
		if mode == 0 || mode == 8 {
			if err != nil || len(states) != 1 || states[0].Delete != (mode == 0) || !sameBinding(states[0].Binding, binding) {
				t.Fatalf("matching decision: states=%+v: %v", states, err)
			}
		} else if err == nil || len(states) != 0 {
			t.Fatalf("nonmatching decision authorized release: mode=%d states=%+v: %v", mode, states, err)
		}
	})
}

func FuzzJobAndReservationShareAward(f *testing.F) {
	for mode := uint8(0); mode < 5; mode++ {
		f.Add(mode, []byte("foreign"))
	}
	f.Fuzz(func(t *testing.T, mode uint8, input []byte) {
		mode %= 5
		l, kube, boundary, control := fixture()
		if claimed, err := l.LaunchOne(t.Context()); err != nil || !claimed {
			t.Fatalf("publish: %t: %v", claimed, err)
		}
		job, err := kube.BatchV1().Jobs(l.Config.Namespace).Get(t.Context(), jobName(control.award.Binding), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		binding, reservation, err := jobOwnership(*job)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(input)
		foreign := "foreign-" + hex.EncodeToString(sum[:8])
		switch mode {
		case 1:
			binding.Holder = foreign
		case 2:
			reservation.Award.Holder = foreign
		case 3:
			reservation.Award.ID = foreign
		case 4:
			reservation.Award.Generation++
		}
		body, err := json.Marshal(binding)
		if err != nil {
			t.Fatal(err)
		}
		job.Annotations[bindingAnnotation] = string(body)
		body, err = json.Marshal(reservation)
		if err != nil {
			t.Fatal(err)
		}
		job.Annotations[reservationAnnotation] = string(body)
		if _, err := kube.BatchV1().Jobs(l.Config.Namespace).Update(t.Context(), job, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		control.remove = true
		err = l.Sync(t.Context())
		if mode == 0 {
			if err != nil || boundary.releases != 1 {
				t.Fatalf("matching Job: releases=%d: %v", boundary.releases, err)
			}
		} else if err == nil || boundary.releases != 0 {
			t.Fatalf("changed award authorized teardown: mode=%d releases=%d: %v", mode, boundary.releases, err)
		}
	})
}

func FuzzBoundaryReleaseRequiresOwnAward(f *testing.F) {
	for mode := uint8(0); mode < 7; mode++ {
		f.Add(mode, []byte("foreign"))
	}
	f.Fuzz(func(t *testing.T, mode uint8, input []byte) {
		mode %= 7
		boundary, kube := boundaryFixture()
		binding := boundaryBinding("team")
		original, _, err := boundary.Reserve(t.Context(), binding, jobName(binding))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(input)
		foreign := "foreign-" + hex.EncodeToString(sum[:8])
		candidate := original
		switch mode {
		case 1:
			candidate.Award.Holder = foreign
		case 2:
			candidate.Award.Generation++
		case 3:
			candidate.Award.ID = foreign
		case 4:
			candidate.Award.Kind = capability.UnitNode
		case 5:
			candidate.NodeUID = foreign
		case 6:
			candidate.ReservationID = foreign
		}
		clears := 0
		boundary.Clear = func(context.Context, CloudNodeReservation) error { clears++; return nil }
		err = boundary.Release(t.Context(), candidate)
		if mode == 0 {
			if err != nil || clears != 1 {
				t.Fatalf("own release: clears=%d: %v", clears, err)
			}
			return
		}
		if err == nil || clears != 0 {
			t.Fatalf("foreign release: mode=%d clears=%d: %v", mode, clears, err)
		}
		node, err := kube.CoreV1().Nodes().Get(t.Context(), original.NodeName, metav1.GetOptions{})
		if err != nil || node.Annotations[reservationAnnotation] == "" {
			t.Fatalf("foreign release lost original reservation: %v", err)
		}
		if err := boundary.Release(t.Context(), original); err != nil || clears != 1 {
			t.Fatalf("original release: clears=%d: %v", clears, err)
		}
	})
}
