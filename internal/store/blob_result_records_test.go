package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func blobResultFixture(t *testing.T) (*Store, *blobRecords, NodeTransition) {
	t.Helper()
	s, records, intent := blobRecordFixture(t)
	output := blobOutputRequest(intent)
	if _, err := records.CommitRecord(t.Context(), output); err != nil {
		t.Fatal(err)
	}
	manifest := intent
	manifest.Digest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := commitTestBlob(t, records.tenant, manifest); err != nil {
		t.Fatal(err)
	}
	members := []blob.Member{{Team: "a", ManifestDigest: manifest.Digest, Digest: intent.Digest, Path: "result", Size: intent.Size, Mode: 0o644}}
	records.manifests = fakeBlobManifests{manifest.Digest: members}
	request := blobOutputRequest(manifest)
	request.Key.Kind, request.Members = blob.KindManifest, members
	if _, err := records.CommitRecord(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	s.resultRecords = records
	a := intent.Identity.Attempt.Value
	actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: intent.Authority.CapabilityRef}, CapabilityPrefix: intent.Authority.CapabilityRef, Holder: "machine", Generation: a.Generation}
	change := NodeTransition{Ref: controllerwire.NodeRef{RunID: a.RunID, NodeID: a.NodeID}, Actor: actor, To: NodeSuccess, Cause: CauseReport, SourceAttempt: sourceAttempt(a.Attempt), Extra: TransitionExtra{Result: &NodeResult{Outcome: NodeSuccess, OutputRecord: &output.Key, ManifestRecord: &request.Key}}}
	return s, records, change
}

func assertBlobResultUnchanged(t *testing.T, records *blobRecords, change NodeTransition) {
	t.Helper()
	row, err := records.tenant.ReadNode(t.Context(), change.Ref)
	if err != nil || row.Status != StatusRunning || row.StateSeq != 0 || row.CapabilityPrefix != change.Actor.CapabilityPrefix {
		t.Fatalf("refused attachment changed job: %+v, %v", row, err)
	}
	var closed int
	if err := records.tenant.s.db.QueryRow("SELECT COUNT(*) FROM node_attempts WHERE finished_at IS NOT NULL").Scan(&closed); err != nil || closed != 0 {
		t.Fatalf("refused attachment closed attempt: %d, %v", closed, err)
	}
	rewriteSchemaCount(t, records.tenant.s.db, "events", 0)
}

func TestBlobResultRecordsCommitAndReplayWithTerminalState(t *testing.T) {
	s, records, change := blobResultFixture(t)
	for i := range 2 {
		applied, err := records.tenant.TransitionNode(t.Context(), change)
		if err != nil || applied.Replayed != (i == 1) {
			t.Fatalf("result %d: %+v, %v", i, applied, err)
		}
	}
	row, err := records.tenant.ReadNode(t.Context(), change.Ref)
	if err != nil || row.Status != StatusDone || row.Outcome != NodeSuccess {
		t.Fatalf("result did not finish job: %+v, %v", row, err)
	}
	var outcome string
	var finished int64
	if err := s.db.QueryRow("SELECT outcome,finished_at FROM node_attempts WHERE team='a' AND run_id='run' AND node_id='job' AND attempt=2 AND generation=7").Scan(&outcome, &finished); err != nil || outcome != "success" || finished != s.now().UnixNano() {
		t.Fatalf("attempt closure: %s, %d, %v", outcome, finished, err)
	}
	rewriteSchemaCount(t, s.db, "events", 1)
	rewriteSchemaCount(t, s.db, "blob_records", 2)
	events, err := records.tenant.ReadStateEvents(t.Context(), change.Ref.RunID, 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("result events: %+v, %v", events, err)
	}
	var persisted NodeResult
	if err := json.Unmarshal(events[0].Payload, &persisted); err != nil || !reflect.DeepEqual(persisted, *change.Extra.Result) {
		t.Fatalf("result references did not persist: %+v, %v", persisted, err)
	}
}

func TestBlobResultRecordsRejectForgedResultKeys(t *testing.T) {
	_, records, change := blobResultFixture(t)
	property := func(value uint32, dimension, part uint8) bool {
		key := *change.Extra.Result.OutputRecord
		if part%2 != 0 {
			key = *change.Extra.Result.ManifestRecord
		}
		forged := fmt.Sprintf("forged-%d", value)
		switch dimension % 12 {
		case 0:
			key.Team = forged
		case 1:
			key.Kind = blob.KindMemo
		case 2:
			key.Name = forged
		case 3:
			key.Scope.Repo = runspec.RepoKey(forged)
		case 4:
			key.Scope.Ref.Ref = forged
		case 5:
			key.Scope.Ref.Verified = true
		case 6:
			key.Scope.Pipeline = forged
		case 7:
			key.Scope.Job = forged
		case 8:
			key.Scope.Key = forged
		case 9:
			key.Scope.Key = fmt.Sprintf("%s/2/7", forged)
		case 10:
			key.Scope.Key = fmt.Sprintf("run/%d/7", 3+uint64(value))
		case 11:
			key.Scope.Key = fmt.Sprintf("run/2/%d", 8+uint64(value))
		}
		result := *change.Extra.Result
		if part%2 == 0 {
			result.OutputRecord = &key
		} else {
			result.ManifestRecord = &key
		}
		request := change
		request.Extra.Result = &result
		_, err := records.tenant.TransitionNode(t.Context(), request)
		assertBlobResultUnchanged(t, records, change)
		return err != nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 240, Rand: rand.New(rand.NewSource(43))}); err != nil {
		t.Fatal(err)
	}
	if _, err := records.tenant.TransitionNode(t.Context(), change); err != nil {
		t.Fatalf("forgery attempts damaged valid records: %v", err)
	}
}

func TestBlobResultRecordsAttachmentUsesWriteGrant(t *testing.T) {
	for _, grant := range []capability.Grant{capability.DataWrite, capability.DataRead, capability.RunsRead} {
		t.Run(string(grant), func(t *testing.T) {
			s, records, change := blobResultFixture(t)
			var c capability.Capability
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				principal, err := tx.ResolveAuthority(t.Context(), change.Actor.Authority)
				c = principal.Capability
				return err
			}); err != nil {
				t.Fatal(err)
			}
			c.Grants = []capability.Grant{grant}
			body, err := json.Marshal(CapabilityRecord{Version: 1, Value: c})
			if err != nil {
				t.Fatal(err)
			}
			grants, err := json.Marshal(c.Grants)
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, "UPDATE capabilities SET authority_json=?,grants=? WHERE prefix=?", body, grants, c.Prefix)
			_, err = records.tenant.TransitionNode(t.Context(), change)
			if (grant == capability.DataWrite) != (err == nil) {
				t.Fatalf("result attachment with %s: %v", grant, err)
			}
			if err != nil {
				assertBlobResultUnchanged(t, records, change)
			}
		})
	}
}

func TestBlobResultRecordsConfiguredAdapterAndEmbeddedManifest(t *testing.T) {
	s, records, change := blobResultFixture(t)
	s.SetResultBlobRecords(records.manifests)
	configured, ok := s.resultRecords.(*blobRecords)
	if !ok || configured.manifests == nil {
		t.Fatal("store did not retain its manifest reader")
	}
	rewriteSchemaExec(t, s.db, "UPDATE blob_records SET manifest_digest=? WHERE kind='output'", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	change.Extra.Result.ManifestRecord = nil
	if _, err := records.tenant.TransitionNode(t.Context(), change); err != nil {
		t.Fatalf("embedded manifest attachment: %v", err)
	}
}

func TestBlobResultRecordsRefusalsRollbackStateAndAttempts(t *testing.T) {
	for _, scenario := range []string{"missing_record", "expired_record", "corrupt_manifest", "missing_member_edge", "changed_size", "conflicting_manifest", "reader_error", "state_error"} {
		t.Run(scenario, func(t *testing.T) {
			s, records, change := blobResultFixture(t)
			switch scenario {
			case "missing_record":
				rewriteSchemaExec(t, s.db, "DELETE FROM blob_records WHERE kind='manifest'")
			case "expired_record":
				rewriteSchemaExec(t, s.db, "UPDATE blob_records SET expires_at=? WHERE kind='manifest'", s.now().UnixNano())
			case "corrupt_manifest":
				records.manifests = fakeBlobManifests{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb": nil}
			case "missing_member_edge":
				rewriteSchemaExec(t, s.db, "DELETE FROM blob_members")
			case "changed_size":
				rewriteSchemaExec(t, s.db, "UPDATE blobs SET size=4 WHERE digest LIKE 'a%'")
			case "conflicting_manifest":
				intent := blobIntent(change.Actor, runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: 2, Generation: 7})
				intent.Digest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
				if _, err := commitTestBlob(t, records.tenant, intent); err != nil {
					t.Fatal(err)
				}
				records.manifests.(fakeBlobManifests)[intent.Digest] = nil
				rewriteSchemaExec(t, s.db, "UPDATE blob_records SET manifest_digest=? WHERE kind='output'", intent.Digest)
			case "reader_error":
				records.manifests = blobManifestFunc(func(context.Context, Team, blob.Digest) ([]blob.Member, error) {
					return nil, errors.New("manifest storage unavailable")
				})
			case "state_error":
				rewriteSchemaExec(t, s.db, "CREATE TRIGGER refuse_result_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'event unavailable'); END")
			}
			if _, err := records.tenant.TransitionNode(t.Context(), change); err == nil {
				t.Fatal("unverified result committed")
			}
			assertBlobResultUnchanged(t, records, change)
		})
	}
}

func TestBlobResultRecordsUseTheOwningTransaction(t *testing.T) {
	s, records, change := blobResultFixture(t)
	rollback := errors.New("caller rolls back result")
	err := s.inBlobTx(t.Context(), func(tx *storeTx) error {
		if _, err := tx.ExecContext(t.Context(), "DELETE FROM blob_members WHERE team='a'"); err != nil {
			return err
		}
		if _, err := transitionNodeTx(t.Context(), tx, "a", change, tx.now()); errors.Is(err, errBlobIOPending) {
			return err
		} else if !errors.Is(err, blob.ErrIntegrity) {
			return fmt.Errorf("result ignored its transaction's removed member: %v", err)
		}
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO blob_members(team,manifest_digest,member_digest) SELECT team,digest,? FROM blob_records WHERE team='a' AND kind='manifest'", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
			return err
		}
		if _, err := transitionNodeTx(t.Context(), tx, "a", change, tx.now()); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	assertBlobResultUnchanged(t, records, change)
	if _, err := records.tenant.TransitionNode(t.Context(), change); err != nil {
		t.Fatalf("owning rollback damaged records: %v", err)
	}
}

func TestBlobResultRecordsManifestReadCannotOutliveHolder(t *testing.T) {
	s, records, change := blobResultFixture(t)
	reader := records.manifests
	base := s.now()
	property := func(remaining, stalled uint8) bool {
		now := base
		s.clock = func() time.Time { return now }
		expiry := now.Add(time.Duration(1+int(remaining)) * time.Nanosecond)
		rewriteSchemaExec(t, s.db, "UPDATE nodes SET lease_expires_at=? WHERE team='a' AND run_id='run' AND node_id='job'", expiry.UnixNano())
		records.manifests = blobManifestFunc(func(ctx context.Context, team Team, digest blob.Digest) ([]blob.Member, error) {
			now = expiry.Add(time.Duration(stalled) * time.Nanosecond)
			return reader.ReadBlobManifest(ctx, team, digest)
		})
		_, err := records.tenant.TransitionNode(t.Context(), change)
		assertBlobResultUnchanged(t, records, change)
		return err != nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 96, Rand: rand.New(rand.NewSource(47))}); err != nil {
		t.Fatal(err)
	}
}
