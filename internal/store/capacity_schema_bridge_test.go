package store

import (
	"errors"
	"testing"
)

// AdmissionSchemaIsolation checks independent stores and replicas against real PostgreSQL locks.
func AdmissionSchemaIsolation(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	for _, name := range []string{"admission:planner", "capacity-admission:7:planner:plan:global:plan"} {
		t.Run(name, func(t *testing.T) {
			first, second := open(t), open(t)
			held, err := first.beginTx(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackOrLog(held)
			if err := tryAdmissionLockTx(t.Context(), held, name); err != nil {
				t.Fatal(err)
			}
			replica, err := first.beginTx(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackOrLog(replica)
			if err := tryAdmissionLockTx(t.Context(), replica, name); !errors.Is(err, errCapacityAdmissionBusy) {
				t.Fatalf("same-store admission did not defer: %v", err)
			}
			independent, err := second.beginTx(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackOrLog(independent)
			if err := tryAdmissionLockTx(t.Context(), independent, name); err != nil {
				t.Fatalf("another schema blocked admission: %v", err)
			}
		})
	}
}
