package main

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestDoctorFixturesKeepRunHistorySeparate(t *testing.T) {
	first, second := doctorHome(t), doctorHome(t)
	withStore(t, first, func(st *store.Store) {
		if err := st.CreateRun(t.Context(), store.Run{
			ID: "fixture-run", Pipeline: "first", Status: "success", StartedAt: time.Unix(100, 0),
		}); err != nil {
			t.Fatal(err)
		}
	})
	withStore(t, second, func(st *store.Store) {
		if _, err := st.GetRun(t.Context(), "fixture-run"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("fresh doctor fixture found another fixture's run: %v", err)
		}
	})
	withStore(t, first, func(st *store.Store) {
		row, err := st.GetRun(t.Context(), "fixture-run")
		if err != nil || row.Pipeline != "first" {
			t.Fatalf("doctor fixture lost its run on reopen: %+v, %v", row, err)
		}
	})
}
