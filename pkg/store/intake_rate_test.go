package store

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestSealedIntakeChecksRunRateAndReplaysWithoutSpendingAgain(t *testing.T) {
	tenant, request, now := intakeFixture(t)
	if err := tenant.s.SetComputeLimit(t.Context(), ComputeLimitRunsPerHour, 1); err != nil {
		t.Fatal(err)
	}
	request.IdempotencyKey = "first"
	first, err := tenant.AdmitRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := tenant.AdmitRun(t.Context(), request)
	if err != nil || !replay.Replayed || replay.RunID != first.RunID {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	request.IdempotencyKey = "second"
	_, err = tenant.AdmitRun(t.Context(), request)
	var refusal *ComputeLimitError
	if !errors.As(err, &refusal) || refusal.Limit != ComputeLimitRunsPerHour || refusal.Principal != "member" || refusal.Observed != 1 {
		t.Fatalf("hourly refusal=%v", err)
	}
	var canonical *apierr.Error
	if !errors.As(err, &canonical) || canonical.Code != apierr.RateLimited {
		t.Fatalf("hourly refusal has no canonical rate_limited code: %v", err)
	}
	rewriteSchemaCount(t, tenant.s.db, "runs", 1)
	rewriteSchemaCount(t, tenant.s.db, "triggers", 1)
	root := authorityRoot("other", "a")
	root.Grants = []capability.Grant{capability.RunsWrite}
	authorityPersist(t, tenant.s, root)
	request.Authority.ID = root.Prefix
	if _, err := tenant.AdmitRun(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	request.Authority.ID = "member"
	*now = now.Add(2 * time.Hour)
	if _, err := tenant.AdmitRun(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

func TestSealedIntakeRateCountsOnlyTheAuthenticatedTeam(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	if err := tenant.s.SetComputeLimit(t.Context(), ComputeLimitRunsPerHour, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AdmitRun(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	other := &Tenant{s: tenant.s, team: "b"}
	root := authorityRoot("other-team", "b")
	root.Subject.ID = "member"
	root.Grants = []capability.Grant{capability.RunsWrite}
	authorityPersist(t, tenant.s, root)
	request.Authority.ID = root.Prefix
	request.Request.Team = "b"
	if _, err := other.AdmitRun(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := other.AdmitRun(t.Context(), request); !errors.Is(err, ErrComputeLimit) {
		t.Fatalf("second foreign-team admission=%v", err)
	}
}

func TestConcurrentSealedIntakeNeverPassesTheHourlyGuard(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	if err := tenant.s.SetComputeLimit(t.Context(), ComputeLimitGlobalRunsPerHour, 3); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := request
			in.IdempotencyKey = fmt.Sprint(i)
			_, err := tenant.AdmitRun(t.Context(), in)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	admitted := 0
	for err := range results {
		if err == nil {
			admitted++
		} else if !errors.Is(err, ErrComputeLimit) {
			t.Fatal(err)
		}
	}
	if admitted != 3 {
		t.Fatalf("admitted %d, want 3", admitted)
	}
	rewriteSchemaCount(t, tenant.s.db, "runs", 3)
	rewriteSchemaCount(t, tenant.s.db, "triggers", 3)
}

func TestSealedIntakeRateKeepsPrincipalKindsSeparateProperty(t *testing.T) {
	property := func(seed uint64) bool {
		tenant, request, _ := intakeFixture(t)
		id := fmt.Sprintf("subject-%016x", seed)
		machine := authorityRoot("machine", "a")
		machine.Subject.ID = id
		machine.Grants = []capability.Grant{capability.RunsWrite}
		authorityPersist(t, tenant.s, machine)
		request.Authority.ID = machine.Prefix
		if err := tenant.s.SetComputeLimit(t.Context(), ComputeLimitRunsPerHour, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := tenant.AdmitRun(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		actor := authorityMember(t, tenant.s, id, RoleEditor)
		request.Authority = actor.Authority
		if _, err := tenant.AdmitRun(t.Context(), request); err != nil {
			t.Logf("machine spent an account's rate budget: %v", err)
			return false
		}
		_, err := tenant.AdmitRun(t.Context(), request)
		return errors.Is(err, ErrComputeLimit)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 20}); err != nil {
		t.Fatal(err)
	}
}
