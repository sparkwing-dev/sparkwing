package controller_test

import (
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestOperatorSettingsDefaultWriterCommitsThroughHTTP(t *testing.T) {
	f := newCronsFixture(t)
	ctx := t.Context()
	for _, kind := range []capability.ProvisionKind{capability.ProvisionBootstrap, capability.ProvisionService} {
		t.Run(string(kind), func(t *testing.T) {
			f := *f
			f.t = t
			raw, _, err := teststore.ProvisionCredential(ctx, f.store, capability.ProvisionSpec{
				Kind: kind, RegistrationID: "service:settings", Subject: capability.Subject{Kind: capability.SubjectService, ID: "settings"},
				Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorLimits, capability.OperatorObjectStore},
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, route := range []struct {
				path string
				body any
			}{
				{"limits", map[string]any{"limits": map[string]int64{store.ComputeLimitGlobalRunners: 7}}},
				{"signups", map[string]any{"mode": "waitlist", "hourly_limit": 12}},
				{"object-store/settings", map[string]any{"run_retention_days": 7}},
				{"object-store/quotas/default", map[string]any{"max_bytes_per_run": 22}},
				{"object-store/quotas/default/allowance", map[string]any{"storage_allowance_bytes": 45}},
			} {
				f.call(http.MethodPut, "/api/v1/operator/"+route.path, raw, route.body, http.StatusOK, nil)
			}
		})
	}
	limits, err := f.store.AsOperator().ComputeLimits(ctx)
	if err != nil || limits.GlobalRunners != 7 {
		t.Fatalf("limits = %+v: %v", limits, err)
	}
	gate, err := f.store.SignUpGate(ctx)
	if err != nil || gate.Mode != store.SignUpWaitlist || gate.Limits.HourlyLimit != 12 || gate.SetBy != "settings" {
		t.Fatalf("gate = %+v: %v", gate, err)
	}
	policy, err := f.store.AsOperator().StorageSettings(ctx)
	if err != nil || policy.RunRetentionDays != 7 {
		t.Fatalf("storage = %+v: %v", policy, err)
	}
	quota, err := teststore.ForTeam(t, f.store, store.DefaultTeam).StorageQuota(ctx)
	if err != nil || quota.MaxBytesPerRun != 22 || quota.AllowanceBytes != 45 {
		t.Fatalf("quota = %+v: %v", quota, err)
	}
}
