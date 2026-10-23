package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestClaimedHostReadsOnlyDeclaredProducerManifestSelectors(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	_ = engineHTTPRoot(t, e, "a", "reader-machine", []capability.Grant{capability.NodesClaim})
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','reader','pending',1,?,?)`, []byte(`{"id":"reader","consumes":[{"producer":"producer"}]}`), []byte(`{}`))
	key := blob.RecordKey{Team: "a", Kind: blob.KindManifest, Scope: blob.Scope{Repo: "gh:1", Job: "producer"}, Name: "result"}
	raw, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,outcome,generation,attempt,finished_at,spec_json,execution_json,manifest_record) VALUES ('a','run','producer','done','success',3,2,1,?,?,?)`, []byte(`{"id":"producer","env":{"PRIVATE":{"value":"private-source-value"}}}`), []byte(`{}`), raw)
	claim, err := e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "reader-machine", Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "reader"}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(e.Store, nil).WithEngineController(e.EngineController).Handler()
	host, err := client.NewNodeHostClient(client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{handler}}, Bearer: func(context.Context) (string, error) { return string(claim.Bearer), nil }}, "run", "reader")
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	snapshot, err := host.ReadSnapshot(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range snapshot.Nodes {
		var declaration nodeproto.JobSpec
		if json.Unmarshal(row.Spec, &declaration) != nil || runspec.NodeID(declaration.ID) != row.Ref.NodeID {
			t.Fatalf("persisted mask namespace has an invalid job: %s", row.Ref.NodeID)
		}
	}
	property := func(value string, declared, terminal bool) bool {
		producer := "other"
		if declared {
			producer = "producer"
		}
		spec, err := json.Marshal(map[string]any{"id": "reader", "consumes": []map[string]string{{"producer": producer}}, "env": map[string]any{"LOCAL": map[string]string{"value": value}}})
		if err != nil {
			t.Fatal(err)
		}
		engineSQL(t, e.Store, `UPDATE nodes SET spec_json=? WHERE run_id='run' AND node_id='reader'`, spec)
		status, outcome := "pending", ""
		var finished, manifest any
		if terminal {
			status, outcome, finished, manifest = "done", "success", int64(1), raw
		}
		engineSQL(t, e.Store, `UPDATE nodes SET status=?,outcome=?,finished_at=?,manifest_record=? WHERE run_id='run' AND node_id='producer'`, status, outcome, finished, manifest)
		snapshot, err := host.ReadSnapshot(t.Context(), "run")
		if err != nil || len(snapshot.Nodes) != 1 || snapshot.Nodes[0].Ref.NodeID != "reader" || string(snapshot.Nodes[0].Spec) != string(spec) {
			return false
		}
		row, err := host.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "producer"})
		if !declared || !terminal {
			return err != nil
		}
		encoded, encodeErr := json.Marshal(row)
		return err == nil && encodeErr == nil && row.ManifestRecord != nil && *row.ManifestRecord == key && row.Status == runstate.StatusDone && (len(row.Spec) == 0 || string(row.Spec) == "null") && row.CapabilityPrefix == "" && row.OwnerExecution == nil && !strings.Contains(string(encoded), "private-source-value")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}
