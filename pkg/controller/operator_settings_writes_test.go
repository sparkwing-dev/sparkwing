package controller

import (
	"context"
	"encoding/hex"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestOperatorSettingsHTTPCommitAuthorityProperty(t *testing.T) {
	s, writer, authority := newOperatorSettingsFixture(t)
	base := authority.principal
	cases := [][2]string{
		{"limits", `{"limits":{"max_global_runners":7}}`},
		{"signups", `{"mode":"waitlist","hourly_limit":12}`},
		{"object-store/settings", `{"run_retention_days":7}`},
		{"object-store/quotas/customer", `{"max_bytes_per_run":22}`},
		{"object-store/quotas/customer/allowance", `{"storage_allowance_bytes":45}`},
	}
	for _, tc := range cases {
		t.Run(tc[0], func(t *testing.T) {
			property := func(input []byte, phase uint8) bool {
				authority.principal = base
				s.WithOperatorSettingsWriter(writer)
				writer.writes, writer.failure, writer.seen = 0, nil, capability.Authorized{}
				want := 200
				switch phase % 5 {
				case 1:
					authority.principal.Capability.Grants = nil
					want = 403
				case 2:
					s.operatorSettingsWrites = nil
					want = 500
				case 3:
					writer.failure = &apierr.Error{Code: apierr.Forbidden, Message: "authority changed"}
					want = 403
				case 4:
					authority.principal.Capability.RevokedAt = &base.Capability.IssuedAt
					want = 401
				}
				path := "/api/v1/operator/" + tc[0] + "?team=foreign-" + hex.EncodeToString(input) + "&actor=forged-" + hex.EncodeToString(input)
				w := operatorRequest(t, s.Handler(), authority.raw, "PUT", path, tc[1], want)
				if w.Code != want || (writer.writes == 1) != (phase%5 == 0) {
					return false
				}
				if phase%5 == 0 || phase%5 == 3 {
					if writer.seen.Principal.Authority != base.Authority || writer.seen.Principal.Capability.Subject != base.Capability.Subject {
						return false
					}
					if tc[0] == "object-store/quotas/customer" || tc[0] == "object-store/quotas/customer/allowance" {
						if writer.seen.Target.Team != "customer" {
							return false
						}
					} else if writer.seen.Target.Team != "" {
						return false
					}
				}
				return true
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 48}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type storeSettingsWriterFake struct {
	OperatorSettingsWriter
	store *store.Store
}

func (f storeSettingsWriterFake) WriteStorageSettings(ctx context.Context, _ capability.Authorized, value store.StorageSettings) error {
	return f.store.AsOperator().SetStorageSettings(ctx, value)
}
func (f storeSettingsWriterFake) WriteStorageQuota(ctx context.Context, a capability.Authorized, value store.StorageQuota) (store.StorageQuota, error) {
	tenant, err := f.store.ForTeam(ctx, store.Team(a.Target.Team))
	if err != nil {
		return store.StorageQuota{}, err
	}
	if err := tenant.SetStorageQuota(ctx, value); err != nil {
		return store.StorageQuota{}, err
	}
	return tenant.StorageQuota(ctx)
}
func (f storeSettingsWriterFake) WriteStorageAllowance(ctx context.Context, a capability.Authorized, value int64) (store.StorageQuota, error) {
	tenant, err := f.store.ForTeam(ctx, store.Team(a.Target.Team))
	if err != nil {
		return store.StorageQuota{}, err
	}
	return tenant.SetStorageAllowance(ctx, value)
}
