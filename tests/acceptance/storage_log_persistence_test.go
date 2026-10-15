package acceptance_test

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func storageLogPersistence(t *testing.T, ordinal uint8, class string) (*store.Tenant, store.SegmentCommit, store.Actor, func() (*store.Store, *store.Tenant)) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "logs.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	commit, err := seedStoreLogBoundary(t.Context(), s.DB())
	if err != nil {
		t.Fatal(err)
	}
	commit.RetentionClass = class
	days, ok := blob.LogClassDays(class)
	if !ok {
		t.Fatal("invalid log class")
	}
	if _, err := s.DB().ExecContext(t.Context(), "UPDATE runs SET log_retention_class=?,log_retention_days=?", class, days); err != nil {
		t.Fatal(err)
	}
	if ordinal%4 != 0 {
		commit = storageLogOwner(t, s, commit, ordinal)
	}
	reader, err := seedStoreLogReader(t.Context(), s.DB(), "log-boundary")
	if err != nil {
		t.Fatal(err)
	}
	tenant := func() *store.Tenant {
		t.Helper()
		tenant, err := s.ForTeam(t.Context(), store.DefaultTeam)
		if err != nil {
			t.Fatal(err)
		}
		return tenant
	}
	reopen := func() (*store.Store, *store.Tenant) {
		t.Helper()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		s, err = store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		return s, tenant()
	}
	return tenant(), commit, reader, reopen
}

func storageLogOwner(t *testing.T, s *store.Store, commit store.SegmentCommit, ordinal uint8) store.SegmentCommit {
	t.Helper()
	_, _, err := seedStorageJobClaim(t.Context(), s, commit)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := s.DB().QueryRowContext(t.Context(), "SELECT authority_json FROM capabilities WHERE prefix='archive-claim'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var record store.CapabilityRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	c := &record.Value
	c.Unit = []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval}[ordinal%4-1]
	c.Purpose, c.Data = capability.AwardPurpose(c.Unit), nil
	c.Grants = []capability.Grant{capability.LogTail}
	c.Binding = capability.Binding{Kind: capability.BindingRun, RunID: commit.Subject.RunID, Generation: int64(ordinal) + 1, Holder: commit.Actor.Holder}
	c.Billing.Unit = capability.BillingUnit(c.Unit)
	unit := store.UnitRef{Kind: c.Unit, RunID: commit.Subject.RunID}
	commit.Subject.Kind, commit.Subject.Attempt = blob.LogOwnerUnit, nil
	commit.Subject.Unit = &blob.LogUnit{Kind: blob.LogUnitKind(c.Unit), Generation: c.Binding.Generation}
	lease := time.Now().Add(time.Hour).UnixNano()
	switch c.Unit {
	case capability.UnitEval:
		c.Grants = []capability.Grant{capability.ExpansionSubmit}
		c.Binding.Kind, c.Binding.NodeID, c.Binding.Generation = capability.BindingNode, "job", 7
		c.Binding.Expansion = &capability.LeaseRef{Kind: c.Unit, Generation: commit.Subject.Unit.Generation, Holder: c.Binding.Holder}
		commit.Subject.Unit.Source = &blob.LogSource{NodeID: "job", Generation: c.Binding.Generation}
		unit.NodeID, unit.SourceGeneration = "job", c.Binding.Generation
		_, err = s.DB().ExecContext(t.Context(), `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,generation,claimed_by,capability_prefix,lease_expires_at,owner_generation)
VALUES (? ,?,'job',7,'running',? ,? ,? ,?,1)`, c.Team, c.Binding.RunID, commit.Subject.Unit.Generation, c.Binding.Holder, c.Prefix, lease)
	default:
		if c.Unit == capability.UnitTrigger {
			c.Binding.Kind, c.Billing.Unit = capability.BindingTrigger, capability.BillingHost
		}
		_, err = s.DB().ExecContext(t.Context(), `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,capability_prefix,generation,lease_expires_at,needs_labels,prefers_labels)
VALUES (?,?,'claimed',?,1,1,?,?,?,?,?,?)`, c.Team, c.Binding.RunID, c.Unit, c.Binding.Holder, c.Prefix, c.Binding.Generation, lease, []byte(`[]`), []byte(`[]`))
	}
	if err != nil {
		t.Fatal(err)
	}
	id, err := json.Marshal(unit)
	if err != nil {
		t.Fatal(err)
	}
	c.UnitID = string(id)
	if c.Binding.Expansion != nil {
		c.Binding.Expansion.ID = c.UnitID
	}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := json.Marshal(c.Grants)
	if err != nil {
		t.Fatal(err)
	}
	var node, expansion any
	if c.Binding.NodeID != "" {
		node, expansion = c.Binding.NodeID, c.Binding.Expansion.Generation
	}
	_, err = s.DB().ExecContext(t.Context(), `UPDATE capabilities SET authority_json=?,grants=?,unit_kind=?,unit_id=?,award_purpose=?,billing_unit=?,bind_kind=?,bind_node=?,bind_generation=?,eval_unit_generation=?,repo_key='',ref_scope=''
WHERE prefix=?`, raw, grants, c.Unit, c.UnitID, c.Purpose, c.Billing.Unit, c.Binding.Kind, node, c.Binding.Generation, expansion, c.Prefix)
	if err != nil {
		t.Fatal(err)
	}
	commit.Actor.Authority.ID, commit.Actor.CapabilityPrefix, commit.Actor.Generation = c.Prefix, c.Prefix, commit.Subject.Unit.Generation
	return commit
}
