package sparkwing_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestAdmitted_ReportsNothingWhereNothingWasInstalled(t *testing.T) {
	t.Parallel()
	//nolint:staticcheck // a nil context is what a caller outside dispatch holds.
	if got, ok := sparkwing.Admitted(nil); ok {
		t.Errorf("Admitted(nil) = %+v, true; want false", got)
	}
	if got, ok := sparkwing.Admitted(context.Background()); ok {
		t.Errorf("Admitted(bare ctx) = %+v, true; want false", got)
	}
}

func TestAdmitted_ReturnsTheInstalledShare(t *testing.T) {
	t.Parallel()
	want := sparkwing.Admission{Cores: 2.5, MemoryBytes: 640 << 20}
	got, ok := sparkwing.Admitted(sparkwingruntime.WithAdmission(context.Background(), want))
	if !ok {
		t.Fatal("Admitted(ctx) = _, false; want the installed share")
	}
	if got != want {
		t.Errorf("Admitted(ctx) = %+v, want %+v", got, want)
	}
}
