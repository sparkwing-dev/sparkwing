package store_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestNodeSpecReadersPreserveSealedDefaultsAndMetadata(t *testing.T) {
	for _, dialect := range []struct {
		name   string
		target func(*testing.T) *storetest.Target
	}{{"sqlite", storetest.NewSQLite}, {"postgres", storetest.NewPostgres}} {
		t.Run(dialect.name, func(t *testing.T) {
			s := dialect.target(t).Open(t, store.WithPolicies(sealStorePolicies))
			plan := admitArgumentRun(t, s, "readers", []nodewire.JobSpec{
				{ID: "build", Steps: []nodewire.StepSpec{{ID: "body"}}},
				{ID: "alpha", Steps: []nodewire.StepSpec{{ID: "body"}}, Needs: []string{"build"}},
			})
			tenant := teamOf(t, s, plan)
			want := nodewire.JobSpec{
				ID: "alpha", Steps: []nodewire.StepSpec{{ID: "first"}, {ID: "second"}}, Needs: []string{"build"},
				Memo: &nodewire.MemoSpec{}, Children: []nodewire.ChildPipeline{{Pipeline: "child"}},
				Approval: &nodewire.ApprovalSpec{}, Closures: nodewire.ClosureSpec{SkipIf: []string{"guard"}, MemoKey: "memo"},
				Arguments: []nodewire.ArgumentDescription{jobArgument()},
			}
			raw, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			hash, err := runspec.CanonicalHash(want)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().ExecContext(t.Context(), storetest.Rebind(s, `UPDATE nodes SET spec_json=?,spec_hash=? WHERE team=? AND run_id=? AND node_id='alpha'`), raw, string(hash), tenant.Team(), plan.RunID); err != nil {
				t.Fatal(err)
			}
			if err := s.ResealNodeArgumentsForTest(t.Context(), tenant.Team(), plan.RunID, "alpha"); err != nil {
				t.Fatal(err)
			}
			var stored []byte
			if err := s.DB().QueryRowContext(t.Context(), storetest.Rebind(s, `SELECT spec_json FROM nodes WHERE team=? AND run_id=? AND node_id='alpha'`), tenant.Team(), plan.RunID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			var document struct {
				Envelope string `json:"sealed_arguments"`
			}
			if json.Unmarshal(stored, &document) != nil || document.Envelope == "" || bytes.Contains(stored, []byte(argJobDefault)) {
				t.Fatal("fixture did not persist an encrypted default")
			}
			assertSpec := func(t *testing.T, raw []byte) {
				t.Helper()
				var got nodewire.JobSpec
				if bytes.Contains(raw, []byte(document.Envelope)) || bytes.Contains(raw, []byte(`"sealed_arguments"`)) {
					t.Fatal("node reader returned envelope ciphertext")
				}
				if json.Unmarshal(raw, &got) != nil || !reflect.DeepEqual(got, want) {
					t.Fatal("node reader lost readable metadata or decoded defaults")
				}
			}
			t.Run("node", func(t *testing.T) {
				row, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: "alpha"})
				if err != nil {
					t.Fatal(err)
				}
				assertSpec(t, row.Spec)
			})
			t.Run("snapshot", func(t *testing.T) {
				snapshot, err := tenant.ReadSnapshot(t.Context(), plan.RunID)
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range snapshot.Nodes {
					if row.Ref.NodeID == "alpha" {
						assertSpec(t, row.Spec)
						return
					}
				}
				t.Fatal("snapshot lost node")
			})
			readSteps := func(t *testing.T) {
				t.Helper()
				steps, err := tenant.ListDeclaredSteps(t.Context(), plan.RunID)
				if err != nil || !slices.Equal(steps["alpha"], []string{"first", "second"}) {
					t.Fatalf("declared steps changed: %v", err)
				}
			}
			t.Run("declared-steps", readSteps)
			s.CheckNodeSpecMetadataReadersForTest(t, tenant.Team(), plan.RunID, "alpha")
			t.Run("without-argument-key", func(t *testing.T) {
				s.WithoutNodeArgumentKeyForTest(t)
				t.Run("declared-steps", readSteps)
				s.CheckNodeSpecMetadataReadersForTest(t, tenant.Team(), plan.RunID, "alpha")
				t.Run("node-fails-closed", func(t *testing.T) {
					row, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: "alpha"})
					if err == nil || len(row.Spec) != 0 {
						t.Fatal("node reader exposed a spec without decoding its defaults")
					}
				})
				t.Run("snapshot-fails-closed", func(t *testing.T) {
					snapshot, err := tenant.ReadSnapshot(t.Context(), plan.RunID)
					if err == nil || len(snapshot.Nodes) != 0 {
						t.Fatal("snapshot exposed nodes without decoding their defaults")
					}
				})
			})
		})
	}
}
