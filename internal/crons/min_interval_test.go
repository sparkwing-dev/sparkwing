package crons_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRefuseBelowMinInterval(t *testing.T) {
	if err := crons.RefuseBelowMinInterval("*/5 * * * *", 0); err != nil {
		t.Fatalf("an unset guard refused %v", err)
	}
	if err := crons.RefuseBelowMinInterval("", 900); err != nil {
		t.Fatalf("an empty expression refused %v", err)
	}
	if err := crons.RefuseBelowMinInterval("0 * * * *", 900); err != nil {
		t.Fatalf("an hourly schedule refused %v", err)
	}
	err := crons.RefuseBelowMinInterval("*/5 * * * *", 900)
	var refused *store.ComputeLimitError
	if !errors.As(err, &refused) || refused.Limit != store.ComputeLimitCronSeconds {
		t.Fatalf("a five-minute schedule under a fifteen-minute guard = %v", err)
	}
	if refused.Observed != 300 || refused.Cap != 900 {
		t.Fatalf("refusal = %+v, want 300 observed against 900", refused)
	}
}

// safety: a guard set after a schedule was armed still binds it, so the tick
// measures the cadence again rather than trusting arming time.
func TestTickRefusesAScheduleBelowTheMinInterval(t *testing.T) {
	svc := pushedService(t)
	svc.Side = store.CronWhereController
	ctx := context.Background()
	if _, err := svc.ArmPushed(ctx, crons.ArmPush{
		RepoURL: pushedRepoURL,
		Branch:  "main",
		SHA:     "0123456789abcdef0123456789abcdef01234567",
		Entries: []crons.Declared{declaredEntry("sweep", "default", "*/5 * * * *")},
	}); err != nil {
		t.Fatalf("ArmPushed: %v", err)
	}
	if err := svc.Store.SetComputeLimit(ctx, store.ComputeLimitCronSeconds, 900); err != nil {
		t.Fatalf("set the guard: %v", err)
	}

	report, err := svc.Tick(ctx, true)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if report.Failed != 1 || report.Fired != 0 {
		t.Fatalf("report = %+v, want the schedule refused and nothing fired", report)
	}
	if len(report.Errors) != 1 || !strings.Contains(report.Errors[0], "min_cron_interval_seconds") {
		t.Fatalf("errors = %v, want the guard named", report.Errors)
	}

	if err := svc.Store.SetComputeLimit(ctx, store.ComputeLimitCronSeconds, 0); err != nil {
		t.Fatalf("clear the guard: %v", err)
	}
	report, err = svc.Tick(ctx, true)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if report.Failed != 0 {
		t.Fatalf("report = %+v, want no refusal once the guard is cleared", report)
	}
}
