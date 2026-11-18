package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestCloudMemoBilling(t *testing.T) { CloudMemoBilling(t, authorityStore) }

func CloudMemoBilling(t *testing.T, open func(*testing.T) *Store) {
	for _, test := range []struct {
		name, evidence string
		outcome        runstate.NodeState
		want           int64
	}{
		{"memo", "memo", runstate.NodeMemoized, 0},
		{"historical_forged_event", "memo", runstate.NodeMemoized, 600},
		{"retry_after_memo", "memo", runstate.NodeMemoized, 600},
		{"body_after_memo", "memo", runstate.NodeMemoized, 600},
		{"wrong_attempt", "memo", runstate.NodeMemoized, 600},
		{"missing_source", "memo", runstate.NodeMemoized, 600},
		{"skip_if", "skip_if", runstate.NodeSkipped, 600},
		{"forged_header", "memo", runstate.NodeMemoized, 600},
		{"memo_without_copy", "memo", runstate.NodeMemoized, 600},
		{"reported_memo", "", runstate.NodeMemoized, 600},
		{"reported_skip", "", runstate.NodeSkipped, 600},
		{"body", "", runstate.NodeSuccess, 600},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, demand, now := meteredUnitFixtureStore(t, open(t), true)
			claim, actor := meteredUnitClaim(t, s, demand)
			tenant := &Tenant{s: s, team: claim.Team}
			admitExecution(t, tenant, actor)
			ref := runspec.NodeRef{RunID: "run", NodeID: "job"}
			if _, err := tenant.TransitionNode(t.Context(), StartNode{Ref: ref, Actor: actor, SourceAttempt: sourceAttempt(0)}); err != nil {
				t.Fatal(err)
			}
			ordinal := int64(1)
			var output *blob.RecordKey
			if test.evidence == "memo" && test.name != "forged_header" {
				s.testOnlyExec(t, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, []byte(`{"id":"job","memo":{}}`))
				setBlobRecordRef(t, s, actor.CapabilityPrefix)
				intent := blobIntent(actor, runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Generation: int64(actor.Generation), Attempt: 1})
				if err := s.inTx(t.Context(), func(tx *storeTx) error {
					actual, err := resolveBlobAuthorityTx(t.Context(), tx, tenant.team, blob.Authority{CapabilityRef: actor.CapabilityPrefix}, capability.DataWrite)
					intent.Authority, intent.Identity = actual.authority, actual.source
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := commitTestBlob(t, tenant, intent); err != nil {
					t.Fatal(err)
				}
				records := &blobRecords{tenant: tenant}
				s.resultRecords = records
				memo := blobOutputRequest(intent)
				memo.Key.Kind, memo.Key.Name, memo.Key.Scope.Key = blob.KindMemo, "output", fixtureMemoHash
				original, err := records.CommitRecord(t.Context(), memo)
				if err != nil {
					t.Fatal(err)
				}
				activateMemoFixture(t, s)
				if test.name == "historical_forged_event" {
					own := blobOutputRequest(intent)
					if _, err := records.CommitRecord(t.Context(), own); err != nil {
						t.Fatal(err)
					}
					output = &own.Key
					forged := memoServedReceipt{Attempt: intent.Identity.Attempt.Value, Source: original.Key, Digest: original.Digest, Output: runspec.Optional[blob.Digest]{Set: true, Value: intent.Digest}}
					if err := s.inTx(t.Context(), func(tx *storeTx) error {
						_, err := appendEventTx(t.Context(), tx, tenant.team, "run", "job", runstate.EventMemoServed, forged, tx.now(t.Context()), true)
						return err
					}); err != nil {
						t.Fatal(err)
					}
				}
				if test.name != "memo_without_copy" && test.name != "historical_forged_event" {
					lookup := blob.MemoLookup{Authority: intent.Authority, Key: blob.MemoKey{Repo: "gh:1", Ref: intent.Authority.Ref.Scope, Pipeline: "ci", Job: "job", Hash: fixtureMemoHash}}
					if hit, err := records.CopyMemo(t.Context(), blob.CopyMemoRequest{Lookup: lookup, Destination: intent.Identity.Attempt.Value}); err != nil || !hit.Found {
						t.Fatalf("copy hit=%+v: %v", hit, err)
					}
					key := blobOutputRequest(intent).Key
					output = &key
					if test.name == "memo" {
						if hit, err := records.CopyMemo(t.Context(), blob.CopyMemoRequest{Lookup: lookup, Destination: intent.Identity.Attempt.Value}); err != nil || !hit.Found {
							t.Fatalf("copy replay=%+v: %v", hit, err)
						}
						var count int
						if err := s.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM events WHERE team='a' AND kind='memo_served'`).Scan(&count); err != nil || count != 1 {
							t.Fatalf("served-hit receipts=%d: %v", count, err)
						}
					}
					if test.name == "retry_after_memo" {
						*now = now.Add(time.Second)
						row, err := tenant.ReadNode(t.Context(), ref)
						if err != nil {
							t.Fatal(err)
						}
						row.Execution.Retry.Set, row.Execution.Retry.Value = true, 1
						raw, err := json.Marshal(row.Execution)
						if err != nil {
							t.Fatal(err)
						}
						s.testOnlyExec(t, `UPDATE nodes SET execution_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, raw)
						if _, err := tenant.TransitionNode(t.Context(), RetryNode{Ref: ref, Actor: actor, SourceAttempt: sourceAttempt(1), Result: &runstate.NodeResult{Outcome: runstate.NodeFailed}}); err != nil {
							t.Fatal(err)
						}
						ordinal, output = 2, nil
					}

					if test.name == "body_after_memo" {
						if err := tenant.RecordStep(t.Context(), StepReport{Attempt: intent.Identity.Attempt.Value, Actor: actor, StepID: "body", State: "running"}); err != nil {
							t.Fatal(err)
						}
					}
					if test.name == "wrong_attempt" {
						s.testOnlyExec(t, `UPDATE events SET payload=? WHERE team='a' AND kind='memo_served'`, []byte(`{"attempt":{"team":"a","run_id":"run","node_id":"job","attempt":9,"generation":9}}`))
					}
					if test.name == "missing_source" {
						s.testOnlyExec(t, `DELETE FROM blob_records WHERE team='a' AND kind='memo'`)
					}

				}
			}
			*now = now.Add(5 * time.Second)
			if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(time.Second)
			command := ReportNode{Ref: ref, Actor: actor, SourceAttempt: sourceAttempt(ordinal), Outcome: test.outcome, Result: &runstate.NodeResult{Outcome: test.outcome, OutputRecord: output}, BodySkipped: test.evidence}
			if _, err := tenant.TransitionNode(t.Context(), command); err != nil {
				t.Fatal(err)
			}
			unitBillingTotal(t, s, test.want, true)
			if err := tenant.AppendEvent(t.Context(), "run", "job", runstate.EventMemoServed, []byte(`{}`)); err == nil {
				t.Fatal("application forged served-hit event")
			}
			var seconds int64
			if err := s.db.QueryRowContext(t.Context(), `SELECT COALESCE(SUM(seconds),0) FROM credit_charges WHERE team='a'`).Scan(&seconds); err != nil || seconds != test.want/10 {
				t.Fatalf("billed seconds=%d: %v", seconds, err)
			}
			var intervals, attempts int
			if err := s.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM billable_intervals WHERE team='a' AND finished_at IS NOT NULL`).Scan(&intervals); err != nil || intervals != int(ordinal) {
				t.Fatalf("interval evidence=%d: %v", intervals, err)
			}
			if err := s.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM node_attempts WHERE team='a' AND finished_at IS NOT NULL`).Scan(&attempts); err != nil || attempts != int(ordinal) {
				t.Fatalf("attempt evidence=%d: %v", attempts, err)
			}
			if replay, err := tenant.TransitionNode(t.Context(), command); err != nil || !replay.Replayed {
				t.Fatalf("replay=%+v: %v", replay, err)
			}
			unitBillingTotal(t, s, test.want, true)
		})
	}
}
