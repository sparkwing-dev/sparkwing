package localcontroller

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestAdmissionCancellationBindingKeepsTheLocalTenant(t *testing.T) {
	var binding Binding
	c := openFixture(t, func(_ context.Context, b Binding) (Services, error) {
		binding = b
		return serviceFixture(b), nil
	})
	calls := 0
	c.SetAdmissionCancellation(func(context.Context, string) error { calls++; return nil })
	for _, team := range []store.Team{binding.Tenant.Team(), "foreign"} {
		if err := binding.Controller.AfterRunCancellation(t.Context(), team, "same-run-id"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("local admission received %d tenant cancellations, want 1", calls)
	}
}
