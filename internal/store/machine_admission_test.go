package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestMachineAdmissionStateRoundTrip(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	if state, err := st.LoadMachineAdmission(t.Context()); err != nil || state != nil {
		t.Fatalf("empty state = %s, %v", state, err)
	}
	first := []byte(`{"version":1,"ledger":{"arrival_seq":4},"requests":{"key":"ticket"}}`)
	if err := st.SaveMachineAdmission(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st = target.Open(t)
	if state, err := st.LoadMachineAdmission(t.Context()); err != nil || !bytes.Equal(state, first) {
		t.Fatalf("reopened state = %s, %v; want %s", state, err, first)
	}
	second := []byte(`{"version":1,"ledger":{},"requests":{}}`)
	if err := st.SaveMachineAdmission(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if state, err := st.LoadMachineAdmission(t.Context()); err != nil || !bytes.Equal(state, second) {
		t.Fatalf("replaced state = %s, %v; want %s", state, err, second)
	}
	var count int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM local_machine_admission`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("state rows = %d, %v; want one", count, err)
	}
}

func TestMachineAdmissionFailedSaveKeepsCommittedState(t *testing.T) {
	st := storetest.Open(t)
	want := []byte(`{"version":1,"requests":{"old":"queued"}}`)
	if err := st.SaveMachineAdmission(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMachineAdmission(t.Context(), []byte(`{"broken"`)); err == nil {
		t.Fatal("saved malformed JSON")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := st.SaveMachineAdmission(ctx, []byte(`{"version":1}`)); err == nil {
		t.Fatal("saved state after cancellation")
	}
	if state, err := st.LoadMachineAdmission(t.Context()); err != nil || !bytes.Equal(state, want) {
		t.Fatalf("state after failed saves = %s, %v; want %s", state, err, want)
	}
}

func TestMachineAdmissionRestartPreservesQueueOrderAndGrants(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	ledger, err := admission.New(admission.Config{TotalCores: 2})
	if err != nil {
		t.Fatal(err)
	}
	var holder admission.Lease
	for _, id := range []string{"holder", "first", "second"} {
		decision, _, err := ledger.Submit(admission.Request{ID: id, Cores: 2})
		if err != nil {
			t.Fatal(err)
		}
		if id == "holder" {
			if decision.Kind != admission.DecisionGranted {
				t.Fatalf("holder decision = %+v", decision)
			}
			holder = decision.Lease
		} else if decision.Kind != admission.DecisionQueued {
			t.Fatalf("%s decision = %+v", id, decision)
		}
	}
	snapshot := ledger.Snapshot()
	state, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMachineAdmission(t.Context(), state); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st = target.Open(t)
	state, err = st.LoadMachineAdmission(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var restored admission.Snapshot
	if err := json.Unmarshal(state, &restored); err != nil {
		t.Fatal(err)
	}
	ledger, err = admission.Restore(restored, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ledger.Snapshot(); !reflect.DeepEqual(got, snapshot) {
		t.Fatalf("restored state differs:\ngot %+v\nwant %+v", got, snapshot)
	}
	member := "holder"
	for i, id := range []string{"first", "second"} {
		events, err := ledger.Release(holder.ID, member)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 2 || events[1].Kind != admission.EventPromoted || events[1].RequestID != id {
			t.Fatalf("release %d events = %+v; want %s promoted", i, events, id)
		}
		holder.ID = events[1].Lease
		member = id
	}
}
