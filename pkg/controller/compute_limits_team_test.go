package controller_test

import (
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestComputeLimitsShowOnlyTheOperatorGlobalUsage(t *testing.T) {
	f := newIdentityFixture(t)
	a := f.signIn(person("payer", "payer@example.test", "Payer"))
	b := f.signIn(person("neighbor", "neighbor@example.test", "Neighbor"))
	if code := f.call(http.MethodPut, "/api/v1/compute-limits", "Bearer "+f.admin,
		map[string]any{"limits": map[string]int64{store.ComputeLimitConcurrentRunners: 1}}, nil); code != http.StatusOK {
		t.Fatalf("set the concurrent runner guard: %d", code)
	}
	for _, session := range []string{a.SessionID, b.SessionID} {
		var out struct {
			Usage map[string]any `json:"usage"`
		}
		if code := f.call(http.MethodGet, "/api/v1/compute-limits", sessionAuth(session), nil, &out); code != http.StatusOK {
			t.Fatalf("team compute limits: %d", code)
		}
		for _, field := range []string{"runners", "alarm_reached", "by_principal"} {
			if _, ok := out.Usage[field]; ok {
				t.Fatalf("team response exposed global usage %s", field)
			}
		}
	}
	var operator struct {
		Usage map[string]any `json:"usage"`
	}
	if code := f.call(http.MethodGet, "/api/v1/compute-limits", "Bearer "+f.admin, nil, &operator); code != http.StatusOK {
		t.Fatalf("operator compute limits: %d", code)
	}
	for _, field := range []string{"runners", "alarm_reached"} {
		if _, ok := operator.Usage[field]; !ok {
			t.Fatalf("operator response omitted %s", field)
		}
	}
}

func TestComputeLimitsTeamReadDoesNotDependOnGlobalUsage(t *testing.T) {
	f := newIdentityFixture(t)
	member := f.signIn(person("reader", "reader@example.test", "Reader"))
	if _, err := f.store.DB().Exec(`DROP TABLE nodes`); err != nil {
		t.Fatal(err)
	}
	if code := f.call(http.MethodGet, "/api/v1/compute-limits", sessionAuth(member.SessionID), nil, nil); code != http.StatusOK {
		t.Fatalf("team compute limits with unavailable fleet usage = %d, want 200", code)
	}
	if code := f.call(http.MethodGet, "/api/v1/compute-limits", "Bearer "+f.admin, nil, nil); code != http.StatusInternalServerError {
		t.Fatalf("operator compute limits with unavailable fleet usage = %d, want 500", code)
	}
}
