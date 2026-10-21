package store_test

import (
	"errors"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestGitHubCommitStatusModeSurvivesReopen(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	tenant, admitted, err := storePlanSealRequest(t.Context(), st)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.RecordGitHubCheckRun(t.Context(), admitted.RunID, 41); err != nil {
		t.Fatal(err)
	}
	if err := tenant.UseGitHubCommitStatuses(t.Context(), admitted.RunID); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := target.Open(t)
	tenant, err = reopened.ForTeam(t.Context(), tenant.Team())
	if err != nil {
		t.Fatal(err)
	}
	if statuses, err := tenant.GitHubCommitStatuses(t.Context(), admitted.RunID); err != nil || !statuses {
		t.Fatalf("reopened fallback=%t: %v", statuses, err)
	}
	if id, err := tenant.GitHubCheckRun(t.Context(), admitted.RunID); err != nil || id != 41 {
		t.Fatalf("fallback replaced check identity=%d: %v", id, err)
	}
}

func TestGitHubCommitStatusModeIsScopedProperty(t *testing.T) {
	property := func(suffix string, foreign bool) bool {
		st := storetest.Open(t, store.WithPolicies(sealStorePolicies))
		now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
		st.SetTestClock(t, func() time.Time { return now })
		tenant, admitted, err := storePlanSealRequest(t.Context(), st)
		if err != nil {
			t.Fatal(err)
		}
		writer, run := tenant, admitted.RunID+"different"+suffix
		if foreign {
			if err := st.AsOperator().TestOnlyCreateTeam(t.Context(), "foreign"); err != nil {
				t.Fatal(err)
			}
			writer, err = st.ForTeam(t.Context(), "foreign")
			if err != nil {
				t.Fatal(err)
			}
			run = admitted.RunID
		}
		if err := writer.UseGitHubCommitStatuses(t.Context(), run); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("foreign fallback mutation=%v", err)
		}
		if _, err := writer.GitHubCommitStatuses(t.Context(), run); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("foreign fallback read=%v", err)
		}
		statuses, err := tenant.GitHubCommitStatuses(t.Context(), admitted.RunID)
		return err == nil && !statuses
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 16}); err != nil {
		t.Fatal(err)
	}
}
