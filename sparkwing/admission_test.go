package sparkwing_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestAdmitted_ReportsNothingWhereNothingWasInstalled(t *testing.T) {
	t.Parallel()
	//nolint:staticcheck // a nil context is what a caller outside dispatch holds.
	//lint:ignore SA1012 This regression verifies Admitted accepts a nil context outside dispatch.
	if got, ok := sparkwing.Admitted(nil); ok {
		t.Errorf("Admitted(nil) = %+v, true; want false", got)
	}
	if got, ok := sparkwing.Admitted(context.Background()); ok {
		t.Errorf("Admitted(bare ctx) = %+v, true; want false", got)
	}
}
