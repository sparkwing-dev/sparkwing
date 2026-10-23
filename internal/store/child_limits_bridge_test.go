package store

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

func childLimitsFixture(t *testing.T, s *Store, children int) time.Time {
	t.Helper()
	now := s.now()
	for _, team := range []string{"a"} {
		s.testOnlyExec(t, `INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1) ON CONFLICT(name) DO NOTHING`, team)
		s.testOnlyExec(t, `INSERT INTO team_counters(team) VALUES (?) ON CONFLICT(team) DO NOTHING`, team)
	}
	s.testOnlyExec(t, `DELETE FROM runs WHERE team='a'`)
	insert := func(id, parent, root string) {
		lineage := fmt.Sprintf(`{"admission_subject":{"kind":"account","id":"alice"},"parent_run_id":%q,"root_run_id":%q,"parent_job_id":"","retry_of":"","retrier":{"set":false,"value":{"kind":"","id":"","display":""}}}`, parent, root)
		s.testOnlyExec(t, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_subject_kind,created_principal,created_at,parent_run_id,retry_of,lineage_json,log_retention_class,log_retention_days) VALUES ('a',?,(SELECT COALESCE(MAX(number),0)+1 FROM runs),'ci','running',?,'request',1,'gh:1','host','execute',?,?,?,?,?,'',?,'r30',30)`,
			id, []byte(`{"version":1,"request":{"actor":{"kind":"account","id":"alice"}}}`), []byte(`[]`), map[bool]string{true: "account", false: "machine"}[parent == ""], map[bool]string{true: "alice", false: "runner"}[parent == ""], now.UnixNano(), parent, []byte(lineage))
	}
	insert("root", "", "")
	for i := range children {
		insert(fmt.Sprintf("child-%d", i), "root", "root")
	}
	return now
}

// TestOnlyChildLimits is D11 and 10#6 in the store's dialect: a child run is bounded
// in depth and per root, and spends the hourly budget of whoever created its root
// rather than escaping every budget.
func (s *Store) TestOnlyChildLimits(t *testing.T) {
	t.Helper()
	refused := func(err error, limit string) bool {
		var e *apierr.Error
		return errors.As(err, &e) && e.Code == apierr.Conflict && e.Details["limit"] == limit
	}
	childLimitsFixture(t, s, limits.MaxChildrenPerRoot-1)
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		return enforceChildLimitsTx(t.Context(), tx, "a", "root", limits.MaxChildDepth)
	})
	if err != nil {
		t.Fatalf("child within both bounds refused: %v", err)
	}
	err = s.inTx(t.Context(), func(tx *storeTx) error {
		err := enforceChildLimitsTx(t.Context(), tx, "a", "root", limits.MaxChildDepth+1)
		return err
	})
	if !refused(err, "max_child_depth") {
		t.Fatalf("child below the depth bound admitted: %v", err)
	}

	now := childLimitsFixture(t, s, limits.MaxChildrenPerRoot)
	err = s.inTx(t.Context(), func(tx *storeTx) error {
		err := enforceChildLimitsTx(t.Context(), tx, "a", "root", 2)
		return err
	})
	if !refused(err, "max_children_per_root") {
		t.Fatalf("51st child of one root admitted: %v", err)
	}

	if _, err := s.AsOperator().SetComputeLimits(t.Context(), map[string]int64{ComputeLimitRunsPerHour: int64(limits.MaxChildrenPerRoot) + 1}); err != nil {
		t.Fatal(err)
	}
	alice := capability.Subject{Kind: capability.SubjectAccount, ID: "alice"}
	err = s.inTx(t.Context(), func(tx *storeTx) error {
		return enforceRunsPerHourTx(t.Context(), tx, "a", "next", alice, now)
	})
	var hourly *ComputeLimitError
	if !errors.As(err, &hourly) || hourly.Observed != int64(limits.MaxChildrenPerRoot)+1 {
		t.Fatalf("root and its children were not charged to the root's creator: %v", err)
	}
	err = s.inTx(t.Context(), func(tx *storeTx) error {
		return enforceRunsPerHourTx(t.Context(), tx, "a", "next", capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}, now)
	})
	if err != nil {
		t.Fatalf("children were charged to the runner holding the parent claim: %v", err)
	}
}
