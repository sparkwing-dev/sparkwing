package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func TestDashboardStartRefusesABuildWithoutItsBundle(t *testing.T) {
	missing := func() error { return errors.New("missing") }
	for _, args := range [][]string{{"dashboard", "start"}, {"dashboard", "open"}} {
		inv, err := clisurface.Parse("sparkwing", args)
		if err != nil {
			t.Fatal(err)
		}
		err = requireDashboardBundle(inv, missing)
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.Unavailable || !strings.Contains(failure.Hint, "bin/build-web.sh") {
			t.Fatalf("%v: err = %v, want an unavailable refusal naming the build step", args, err)
		}
	}
	inv, err := clisurface.Parse("sparkwing", []string{"dashboard", "status"})
	if err != nil {
		t.Fatal(err)
	}
	if err := requireDashboardBundle(inv, missing); err != nil {
		t.Fatalf("status needs no bundle: %v", err)
	}
}
