package store

import (
	"errors"
	"strings"
	"testing"
)

// On Sparkwing Cloud a team without an explicit quota gets the free tier until it has bought credits, then the paid
// tier; an explicit quota or the deployment's default tier wins, and the operator's own team keeps no tier.
func TestCloudStorageTiersFollowPaymentUnlessOverridden(t *testing.T) {
	s := authorityStore(t)
	ctx := t.Context()
	a, b := &Tenant{s: s, team: "a"}, &Tenant{s: s, team: "b"}
	if q, err := a.StorageQuota(ctx); err != nil || q != (StorageQuota{Team: "a"}) {
		t.Fatalf("self-hosted quota=%+v, %v; want unlimited", q, err)
	}
	s.EnableCloudStorage()
	if q, err := a.StorageQuota(ctx); err != nil || q.Tier != storageTierFree || q.MaxBytesPerRun != freeTierQuota.MaxBytesPerRun {
		t.Fatalf("unfunded quota=%+v, %v; want the free tier", q, err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(team,id,kind,amount_micro,reference,created_by,created_at) VALUES ('a','paid-grant','paid',1000000,'pi_tier','provider',1)`)
	if q, err := a.StorageQuota(ctx); err != nil || q.Tier != storageTierPaid || q.MaxBytesPerMonth != paidTierQuota.MaxBytesPerMonth || q.MaxObjectsPerRun != paidTierQuota.MaxObjectsPerRun {
		t.Fatalf("funded quota=%+v, %v; want the paid tier", q, err)
	}
	if _, err := a.SetStorageAllowance(ctx, 456); err != nil {
		t.Fatal(err)
	}
	if q, err := a.StorageQuota(ctx); err != nil || q.Tier != storageTierPaid || q.AllowanceBytes != 456 {
		t.Fatalf("quota with an allowance=%+v, %v; want the paid tier and the allowance", q, err)
	}
	if err := a.SetStorageQuota(ctx, StorageQuota{MaxBytesPerRun: 5}); err != nil {
		t.Fatal(err)
	}
	if q, err := a.StorageQuota(ctx); err != nil || q.Tier != "" || q.MaxBytesPerRun != 5 || q.MaxBytesPerMonth != 0 || q.AllowanceBytes != 456 {
		t.Fatalf("overridden quota=%+v, %v; want the explicit limits", q, err)
	}
	if err := s.AsOperator().SetStorageSettings(ctx, StorageSettings{DefaultTier: storageTierPaid}); err != nil {
		t.Fatal(err)
	}
	if q, err := b.StorageQuota(ctx); err != nil || q.Tier != storageTierPaid {
		t.Fatalf("default-tier quota=%+v, %v; want the deployment default", q, err)
	}
	operator := &Tenant{s: s, team: DefaultTeam}
	if err := s.AsOperator().SetStorageSettings(ctx, StorageSettings{}); err != nil {
		t.Fatal(err)
	}
	if q, err := operator.StorageQuota(ctx); err != nil || q != (StorageQuota{Team: DefaultTeam}) {
		t.Fatalf("operator team quota=%+v, %v; want unlimited", q, err)
	}
}

// A refusal at a tier limit names the limit and how to raise it.
func TestStorageTierRefusalNamesTheRemedy(t *testing.T) {
	s := authorityStore(t)
	s.EnableCloudStorage()
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		d := uploadQuotaDimensions("", "run", StorageMonth(tx.now(t.Context())), freeTierQuota.MaxBytesPerRun+1, 0)[1]
		d.ceiling = freeTierQuota.MaxBytesPerRun
		_, err := applyStorageQuotaDimensionTx(t.Context(), tx, "a", d, false)
		return err
	})
	var refusal *StorageQuotaError
	if !errors.As(err, &refusal) || !strings.Contains(err.Error(), refusal.Limit) || !strings.Contains(err.Error(), "support@sparkwing.dev") {
		t.Fatalf("refusal = %v; want the limit and the remedy", err)
	}
}
