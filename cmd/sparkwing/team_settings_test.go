package main

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func TestChildCeilingCLI(t *testing.T) {
	for _, value := range []string{"200", "0", "-1", "words", "9223372036854775808"} {
		inv, err := clisurface.Parse("sparkwing", []string{"team", "settings", "set", "--profile", "work", "--max-children-per-root=" + value})
		if err != nil {
			t.Fatal(err)
		}
		update, err := teamSettingsUpdate(inv)
		if value == "200" {
			if err != nil || update.MaxChildrenPerRoot == nil || *update.MaxChildrenPerRoot != 200 || update.MembersCanAddMachines != nil || update.ClaimRanking != nil {
				t.Fatalf("ceiling update: %+v %v", update, err)
			}
		} else if err == nil {
			t.Fatalf("accepted invalid ceiling %q", value)
		}
	}
}
