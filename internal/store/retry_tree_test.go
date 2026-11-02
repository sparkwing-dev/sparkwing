package store_test

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestRetryTreeWalksLongChainsAndCyclesWithinItsTeam(t *testing.T) {
	s := storetest.Open(t)
	own := tenant(t, s, signIn(t, s, "own", "own@example.test").PersonalTeam)
	other := tenant(t, s, signIn(t, s, "other", "other@example.test").PersonalTeam)
	base := time.Unix(1_700_000_000, 0).UTC()
	seed := func(tn *store.Tenant, id, retryOf string, at int) {
		t.Helper()
		run := store.Run{RunSummary: runstate.RunSummary{ID: id, Pipeline: "ci", Status: "success", CreatedAt: base.Add(time.Duration(at) * time.Second), RetryOf: retryOf}}
		if err := tn.TestOnlySeedRun(t.Context(), run); err != nil {
			t.Fatal(err)
		}
	}
	seed(other, "other-root", "", 0)
	seed(own, "root", "other-root", 1)
	want := []string{"root"}
	parent := "root"
	for i := range 300 {
		id := fmt.Sprintf("chain-%03d", i)
		seed(own, id, parent, i+2)
		want = append(want, id)
		parent = id
	}
	seed(own, "sibling", "root", 2)
	want = append(want[:2], append([]string{"sibling"}, want[2:]...)...)
	seed(other, "other-retry", "chain-010", 400)
	seed(other, "other-child", "root", 401)

	check := func(tn *store.Tenant, selector string, want []string) {
		t.Helper()
		members, err := tn.ListRunRetryTree(t.Context(), selector)
		if err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, m := range members {
			got = append(got, m.ID)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("tree of %q = %v, want %v", selector, got, want)
		}
	}
	for _, cycle := range []bool{false, true} {
		if cycle {
			if _, err := s.DB().ExecContext(t.Context(), storetest.Rebind(s, `UPDATE runs SET retry_of=? WHERE id='root'`), parent); err != nil {
				t.Fatal(err)
			}
		}
		for _, selector := range []string{"root", "chain-150", parent, "sibling"} {
			check(own, selector, want)
		}
		for _, selector := range []string{"other-root", "other-retry", "other-child"} {
			check(own, selector, []string{})
		}
		check(other, "other-root", []string{"other-root"})
		check(other, "other-retry", []string{"other-retry"})
		check(other, "other-child", []string{"other-child"})
		check(other, "root", []string{})
	}
}
