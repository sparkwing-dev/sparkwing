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
	if got := sparkwing.Admitted(nil); got != nil {
		t.Errorf("Admitted(nil) = %v; a caller outside dispatch was told nothing", got)
	}
	if got := sparkwing.Admitted(context.Background()); got != nil {
		t.Errorf("Admitted(bare ctx) = %v; a run with no admission reserved nothing", got)
	}
}

func TestAdmitted_ReportsTheChargeTheOrchestratorInstalled(t *testing.T) {
	t.Parallel()
	want := &sparkwing.Admission{Cores: 2.5, MemoryBytes: 640 << 20, Source: "measured"}
	got := sparkwing.Admitted(sparkwingruntime.WithAdmission(context.Background(), want))
	if got == nil {
		t.Fatal("Admitted read nothing back, so a step cannot size itself against its share")
	}
	if *got != *want {
		t.Errorf("Admitted = %+v, want %+v", *got, *want)
	}
}
