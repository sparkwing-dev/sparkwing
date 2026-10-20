package store

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestOutputReportingUsesCommittedResult(t *testing.T) {
	_, records, change := blobResultFixture(t)
	if _, err := records.tenant.TransitionNode(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	for _, read := range []string{"get", "list"} {
		t.Run(read, func(t *testing.T) {
			var nodes []*Node
			if read == "get" {
				n, err := records.tenant.GetNode(t.Context(), change.Ref.RunID, change.Ref.NodeID)
				if err != nil {
					t.Fatal(err)
				}
				nodes = []*Node{n}
			} else {
				var err error
				nodes, err = records.tenant.ListNodes(t.Context(), change.Ref.RunID)
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(nodes) != 1 {
				t.Fatalf("reported jobs: %d", len(nodes))
			}
			body, err := json.Marshal(nodes[0])
			if err != nil {
				t.Fatal(err)
			}
			var report map[string]any
			if err := json.Unmarshal(body, &report); err != nil {
				t.Fatal(err)
			}
			if got := report["output_digest"]; got != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
				t.Fatalf("committed output digest = %v", got)
			}
		})
	}
}

func TestOutputReportingNeverRevealsAnotherResultProperty(t *testing.T) {
	s, records, change := blobResultFixture(t)
	intent := blobIntent(change.Actor, runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: 2, Generation: 7})
	intent.Authority.Ref.Scope = change.Extra.Result.OutputRecord.Scope.Ref
	other := addBlobJob(t, s, intent, "other")
	if _, err := commitTestBlob(t, records.tenant, other); err != nil {
		t.Fatal(err)
	}
	otherOutput := blobOutputRequest(other)
	if _, err := records.tenant.CommitRecord(t.Context(), otherOutput); err != nil {
		t.Fatal(err)
	}
	if _, err := records.tenant.TransitionNode(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	property := func(value uint64, dimension uint8) bool {
		key := *change.Extra.Result.OutputRecord
		foreign := fmt.Sprintf("foreign-%x", value)
		switch dimension % 9 {
		case 0:
			key.Team = foreign
		case 1:
			key.Kind = blob.KindManifest
		case 2:
			key.Scope.Job = foreign
		case 3:
			key.Scope.Key = foreign + "/2/7"
		case 4:
			key.Scope.Pipeline = foreign
		case 5:
			key.Scope.Ref.Ref = foreign
		case 6:
			key.Scope.Ref.Verified = !key.Scope.Ref.Verified
		case 7:
			key.Name = foreign
		case 8:
			key = otherOutput.Key
		}
		body, err := json.Marshal(key)
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, "UPDATE nodes SET output_record=? WHERE team='a' AND run_id='run' AND node_id='job'", body)
		n, err := records.tenant.GetNode(t.Context(), change.Ref.RunID, change.Ref.NodeID)
		if err == nil && n.OutputDigest != "" {
			t.Logf("forged selector revealed output: %+v", key)
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 120, Rand: rand.New(rand.NewSource(45))}); err != nil {
		t.Fatal(err)
	}
}

func TestOutputReportingOmitsCollectedRecord(t *testing.T) {
	s, records, change := blobResultFixture(t)
	if _, err := records.tenant.TransitionNode(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "DELETE FROM blob_records WHERE kind='output'")
	n, err := records.tenant.GetNode(t.Context(), change.Ref.RunID, change.Ref.NodeID)
	if err != nil || n.OutputDigest != "" {
		t.Fatalf("collected output: %+v, %v", n, err)
	}
}
