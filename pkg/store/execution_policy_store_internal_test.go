package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/executionpolicy"
)

const (
	nodeExecutionPolicyVersion        = executionpolicy.NodeExecutionPolicyVersion
	assistedBodyProtocolVersion       = executionpolicy.AssistedBodyProtocolVersion
	fleetSupervisorRuntimeRequirement = "fleet-supervisor-v1"
	fleetBodyRuntimeRequirement       = "fleet-body-v1"
)

func TestMarkNodeReadyWithExecutionPolicySealsAtomically(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateRun(ctx, Run{ID: "run", Pipeline: "release", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, Node{RunID: "run", NodeID: "deploy", Status: "pending", Deps: []string{"build"}, RequiredExecutorLocation: "cloud"}); err != nil {
		t.Fatal(err)
	}

	policy := testExecutionPolicy()
	if err := st.markNodeReadyWithExecutionPolicy(ctx, "run", "deploy", policy); err != nil {
		t.Fatalf("seal: %v", err)
	}
	node, err := st.GetNode(ctx, "run", "deploy")
	if err != nil {
		t.Fatal(err)
	}
	record := mustNodeRecord(t, st, "run", "deploy")
	persisted, err := nodeExecutionPolicyPersistence(record)
	if err != nil {
		t.Fatal(err)
	}
	if node.ReadyAt == nil || persisted.PolicyHash == "" ||
		persisted.PolicyVersion != nodeExecutionPolicyVersion || persisted.BodyProtocol != assistedBodyProtocolVersion {
		t.Fatalf("sealed node = %+v", node)
	}
	if err := st.markNodeReadyWithExecutionPolicy(ctx, "run", "deploy", policy); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if err := st.UpdateNodeDeps(ctx, "run", "deploy", []string{"other"}); !errors.Is(err, errExecutionPolicyConflict) {
		t.Fatalf("post-seal deps rewrite = %v, want conflict", err)
	}
	changed := testExecutionPolicy()
	changed.SecretNames = append(changed.SecretNames, "OTHER_TOKEN")
	if err := st.markNodeReadyWithExecutionPolicy(ctx, "run", "deploy", changed); !errors.Is(err, errExecutionPolicyConflict) {
		t.Fatalf("changed replay = %v, want conflict", err)
	}
}

func TestExecutionPolicyInvalidSealRollsBackReadyAndTuple(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateRun(ctx, Run{ID: "run", Pipeline: "release", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, Node{RunID: "run", NodeID: "deploy", Status: "pending", Deps: []string{"build"}, RequiredExecutorLocation: "cloud"}); err != nil {
		t.Fatal(err)
	}
	policy := testExecutionPolicy()
	policy.Dependencies[0].NodeID = "wrong"
	if err := st.markNodeReadyWithExecutionPolicy(ctx, "run", "deploy", policy); !errors.Is(err, errExecutionPolicyInvalid) {
		t.Fatalf("invalid seal = %v", err)
	}
	node, err := st.GetNode(ctx, "run", "deploy")
	if err != nil {
		t.Fatal(err)
	}
	record := mustNodeRecord(t, st, "run", "deploy")
	persisted, err := nodeExecutionPolicyPersistence(record)
	if err != nil {
		t.Fatal(err)
	}
	if node.ReadyAt != nil || nodeHasExecutionPolicy(record) || persisted.PolicyHash != "" || persisted.PolicyVersion != 0 {
		t.Fatalf("invalid seal partially committed: %+v", node)
	}
}

func TestSQLExecutionPolicyBindingTamperingFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	for name, tamper := range map[string]func(*testing.T, *Store){
		"pipeline": func(t *testing.T, st *Store) {
			if _, err := st.DB().Exec(`UPDATE runs SET pipeline = 'other' WHERE id = 'run'`); err != nil {
				t.Fatal(err)
			}
		},
		"node": func(t *testing.T, st *Store) {
			if _, err := st.DB().Exec(`UPDATE nodes SET node_id = 'other' WHERE run_id = 'run' AND node_id = 'deploy'`); err != nil {
				t.Fatal(err)
			}
		},
		"deps": func(t *testing.T, st *Store) {
			if _, err := st.DB().Exec(`UPDATE nodes SET deps_json = '["other"]' WHERE run_id = 'run' AND node_id = 'deploy'`); err != nil {
				t.Fatal(err)
			}
		},
		"placement": func(t *testing.T, st *Store) {
			if _, err := st.DB().Exec(`UPDATE nodes SET required_executor_location = 'local' WHERE run_id = 'run' AND node_id = 'deploy'`); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.CreateRun(ctx, Run{ID: "run", Pipeline: "release", Status: "running"}); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateNode(ctx, Node{RunID: "run", NodeID: "deploy", Status: "pending", Deps: []string{"build"}, RequiredExecutorLocation: "cloud"}); err != nil {
				t.Fatal(err)
			}
			if err := st.markNodeReadyWithExecutionPolicy(ctx, "run", "deploy", testExecutionPolicy()); err != nil {
				t.Fatal(err)
			}
			tamper(t, st)
			nodeID := "deploy"
			if name == "node" {
				nodeID = "other"
			}
			if _, err := st.GetNode(ctx, "run", nodeID); !errors.Is(err, errExecutionPolicyInvalid) {
				t.Fatalf("tampered %s binding read = %v, want invalid policy", name, err)
			}
		})
	}
}

func TestSQLExecutionPolicyGetAndListReturnImmutableCopies(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateRun(ctx, Run{ID: "run", Pipeline: "release", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, Node{RunID: "run", NodeID: "deploy", Status: "pending", Deps: []string{"build"}, RequiredExecutorLocation: "cloud"}); err != nil {
		t.Fatal(err)
	}
	if err := st.markNodeReadyWithExecutionPolicy(ctx, "run", "deploy", testExecutionPolicy()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetNode(ctx, "run", "deploy"); err != nil {
		t.Fatal(err)
	}
	wantHash := mustNodeExecutionPolicyPersistence(t, mustNodeRecord(t, st, "run", "deploy")).PolicyHash

	got, err := st.GetNode(ctx, "run", "deploy")
	if err != nil {
		t.Fatal(err)
	}
	got.Deps[0] = "mutated"
	listed, err := st.ListNodes(ctx, "run")
	if err != nil {
		t.Fatal(err)
	}
	listed[0].Deps[0] = "listed-mutation"

	again, err := st.GetNode(ctx, "run", "deploy")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(again.Deps, []string{"build"}) ||
		mustNodeExecutionPolicyPersistence(t, mustNodeRecord(t, st, "run", "deploy")).PolicyHash != wantHash {
		t.Fatalf("returned copies mutated SQL state: %+v", again)
	}
}

func TestOrdinaryReadyIsDenyAllAndClaimSkipsAnyPolicyTuple(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateRun(ctx, Run{ID: "run", Pipeline: "release", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, Node{RunID: "run", NodeID: "assisted", Status: "pending", RequiredExecutorLocation: "cloud"}); err != nil {
		t.Fatal(err)
	}
	policy := testExecutionPolicy()
	policy.NodeID = "assisted"
	policy.Dependencies = nil
	if err := st.markNodeReadyWithExecutionPolicy(ctx, "run", "assisted", policy); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, Node{RunID: "run", NodeID: "ordinary", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "run", "ordinary"); err != nil {
		t.Fatal(err)
	}
	policy.NodeID = "ordinary"
	policy.AllowedLocations = []string{"cloud", "local"}
	if err := st.markNodeReadyWithExecutionPolicy(ctx, "run", "ordinary", policy); !errors.Is(err, errExecutionPolicyConflict) {
		t.Fatalf("upgrade ordinary ready row = %v, want conflict", err)
	}
	claimed, err := st.ClaimNextReadyNode(ctx, ClaimIdentity{}, "runner", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.NodeID != "ordinary" {
		t.Fatalf("claim = %s, want ordinary", claimed.NodeID)
	}

	partialValues := map[string]any{
		"execution_policy_json":                  []byte(`{}`),
		"execution_policy_hash":                  "sha256:" + strings.Repeat("a", 64),
		"execution_policy_version":               1,
		"execution_body_protocol":                1,
		"execution_supervisor_requirements_json": []byte(`[]`),
		"execution_supervisor_requirements_hash": "sha256:" + strings.Repeat("b", 64),
		"execution_body_requirements_json":       []byte(`[]`),
		"execution_body_requirements_hash":       "sha256:" + strings.Repeat("c", 64),
	}
	for column, value := range partialValues {
		t.Run(column, func(t *testing.T) {
			nodeID := "partial-" + column
			if err := st.CreateNode(ctx, Node{RunID: "run", NodeID: nodeID, Status: "pending"}); err != nil {
				t.Fatal(err)
			}
			if _, err := st.DB().Exec(`UPDATE nodes SET `+column+` = ? WHERE run_id = ? AND node_id = ?`, value, "run", nodeID); err != nil {
				t.Fatal(err)
			}
			if _, err := st.GetNode(ctx, "run", nodeID); !errors.Is(err, errExecutionPolicyInvalid) {
				t.Fatalf("read partial tuple = %v, want invalid", err)
			}
			if err := st.MarkNodeReady(ctx, "run", nodeID); !errors.Is(err, errExecutionPolicyInvalid) {
				t.Fatalf("ordinary ready over partial tuple = %v, want invalid", err)
			}
		})
	}
}

func TestNodeJSONNeverContainsExecutionSeal(t *testing.T) {
	policy := testExecutionPolicy()
	policy.Pipeline = "sentinel-private-policy-pipeline"
	policy.SupervisorRequirements = append(policy.SupervisorRequirements, "sentinel-private-supervisor")
	policy.Body.RuntimeRequirements = append(policy.Body.RuntimeRequirements, "sentinel-private-body")
	record := nodeRecord{Node: Node{NodeID: "sentinel"}}
	if err := setNodeExecutionPolicy(&record, policy); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(record.Node)
	if err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range []string{"execution_policy", "sentinel-private-policy-pipeline", "sentinel-private-supervisor", "sentinel-private-body"} {
		if strings.Contains(string(raw), sentinel) {
			t.Fatalf("Node JSON leaked %q: %s", sentinel, raw)
		}
	}
}

func mustNodeExecutionPolicyPersistence(t *testing.T, node *nodeRecord) executionPolicyPersistence {
	t.Helper()
	persisted, err := nodeExecutionPolicyPersistence(node)
	if err != nil {
		t.Fatal(err)
	}
	return persisted
}

func mustNodeRecord(t *testing.T, st *Store, runID, nodeID string) *nodeRecord {
	t.Helper()
	record := &nodeRecord{}
	if err := scanNodeRow(st.queryRow(context.Background(), `SELECT `+nodeSelectColumns+`
 FROM nodes WHERE run_id = ? AND node_id = ?`, runID, nodeID), record); err != nil {
		t.Fatal(err)
	}
	return record
}

func testExecutionPolicy() nodeExecutionPolicy {
	digest := "sha256:" + strings.Repeat("a", 64)
	return nodeExecutionPolicy{
		Version: nodeExecutionPolicyVersion, Pipeline: "release", NodeID: "deploy", AllowedLocations: []string{"cloud"},
		Dependencies:           []nodeDependencyAuthority{{NodeID: "build", WholeOutput: true, MaxBytes: 1 << 20}},
		SecretNames:            []string{"DEPLOY_TOKEN"},
		ResultMaxBytes:         1 << 20,
		SupervisorRequirements: []string{fleetSupervisorRuntimeRequirement},
		Body: nodeCompiledBodyAuthority{
			ProtocolVersion: assistedBodyProtocolVersion, RuntimeRequirements: []string{fleetBodyRuntimeRequirement},
			Source: nodeBodySourceAuthority{Kind: "git", Identity: strings.Repeat("b", 40), ManifestDigest: digest, PlanDigest: digest},
		},
	}
}
