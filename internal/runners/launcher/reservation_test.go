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
	for mode := uint8(0); mode < 8; mode++ {
		f.Add(mode)
	}
	f.Fuzz(func(t *testing.T, mode uint8) {
		mode %= 8
		unit := wireAward(capability.UnitNode)
		unit.Capability = ""
		binding, err := cloudBinding(unit, capability.DeliveryBootstrap)
		if err != nil {
			t.Fatal(err)
		}
		observation := CloudJobObservation{Binding: binding, JobName: jobName(binding), NodeUID: "node", ReservationID: "reservation"}
		fence := binding.LeaseRef()
		control, err := NewControllerControl(launcherWireFake{sync: func(_ context.Context, request controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
			if len(request.Observed) != 1 || request.Observed[0] != (capability.LauncherObservation{Award: binding.LeaseRef(), Reservation: true}) {
				t.Fatalf("reservation changed: %+v", request)
			}
			switch mode {
			case 1:
				fence.Holder = "other"
			case 2:
				fence.Generation++
			case 3:
				fence.ID = "other"
			case 4:
				fence.Kind = capability.UnitTrigger
			case 5:
				return controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{unit}}, nil
			case 6:
				return controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{unit}, Retired: []capability.LeaseRef{fence}}, nil
			case 7:
				return controllerwire.LauncherSyncResponse{Retired: []capability.LeaseRef{fence}}, errors.New("uncertain response")
			}
			return controllerwire.LauncherSyncResponse{Retired: []capability.LeaseRef{fence}}, nil
		}}, controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{unit.Unit}})
		if err != nil {
			t.Fatal(err)
		}
		states, err := control.Sync(t.Context(), []CloudJobObservation{observation})
		if mode == 0 || mode == 5 {
			if err != nil || len(states) != 1 || states[0].Delete != (mode == 0) {
				t.Fatalf("matching decision: %+v %v", states, err)
			}
		} else if err == nil || len(states) != 0 {
			t.Fatalf("foreign retirement: %+v %v", states, err)
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
		} else if err != nil || boundary.releases != 0 {
			t.Fatalf("malformed award must discard Job without releasing reservation: mode=%d releases=%d: %v", mode, boundary.releases, err)
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
		original, err := boundary.Reserve(t.Context(), binding, jobName(binding))
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
		err = boundary.Release(t.Context(), candidate)
		if mode == 0 {
			if err != nil {
				t.Fatalf("own release: %v", err)
			}
			return
		}
		if err == nil {
			t.Fatalf("foreign release: mode=%d was accepted", mode)
		}
		node, err := kube.CoreV1().Nodes().Get(t.Context(), original.NodeName, metav1.GetOptions{})
		if err != nil || node.Annotations[reservationAnnotation] == "" {
			t.Fatalf("foreign release lost original reservation: %v", err)
		}
		if err := boundary.Release(t.Context(), original); err != nil {
			t.Fatalf("original release: %v", err)
		}
	})
}
