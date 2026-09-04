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

func TestOrdinaryReadyIsDenyAllAndLegacyClaimSkipsAnyPolicyTuple(t *testing.T) {
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
	claimed, err := st.ClaimNextReadyNode(ctx, ClaimIdentity{}, "legacy", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.NodeID != "ordinary" {
		t.Fatalf("legacy claim = %s, want ordinary", claimed.NodeID)
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

func TestAgentLossRetryUsesDurableExecutionPolicySnapshot(t *testing.T) {
	t.Run("ignores live source and caller mutation after snapshot", func(t *testing.T) {
		st, retryID, policy := createSealedAgentLossRetry(t)
		defer st.Close()
		ctx := context.Background()
		if _, err := st.DB().Exec(`UPDATE nodes SET deps_json = '["widened"]', needs_labels = '["location=local"]',
			required_coordinator_id = 'mutated', required_executor_location = 'local', attempts_consumed = 99,
			avoid_coordinator_id = 'mutated', avoid_executor_kind = 'gateway', avoid_executor_id = 'mutated',
			execution_policy_hash = 'sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'
			WHERE run_id = 'source' AND node_id = 'deploy'`); err != nil {
			t.Fatal(err)
		}
		callerNode := retryNodeFromSnapshot(retryID)
		callerNode.Deps = []string{"attacker-controlled"}
		callerNode.NeedsLabels = []string{"location=local"}
		callerNode.PrefersLabels = []string{"attacker-preference"}
		callerNode.RequestedCores = 999
		callerNode.RequestedMemoryBytes = 999
		callerNode.RequestedSlots = 999
		if err := st.CreateNode(ctx, callerNode); err != nil {
			t.Fatal(err)
		}
		node, err := st.GetNode(ctx, retryID, "deploy")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(node.Deps, []string{"build"}) || !slices.Equal(node.NeedsLabels, []string{"location=cloud", "gpu"}) ||
			!slices.Equal(node.PrefersLabels, []string{"fast"}) || node.RequestedCores != 2 || node.RequestedMemoryBytes != 1<<30 ||
			node.RequestedSlots != 1 ||
			node.RequiredCoordinatorID != "coordinator-home" || node.RequiredExecutorLocation != "cloud" || node.AttemptsConsumed != 1 ||
			node.AvoidCoordinatorID != "coordinator-old" ||
			mustNodeExecutionPolicyPersistence(t, mustNodeRecord(t, st, retryID, "deploy")).PolicyHash != mustSealExecutionPolicy(t, policy).Hash {
			t.Fatalf("retry materialized from mutable source: %+v", node)
		}
		if err := st.markNodeReadyWithExecutionPolicy(ctx, retryID, "deploy", policy); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("survives source pruning and rejects widening", func(t *testing.T) {
		st, retryID, policy := createSealedAgentLossRetry(t)
		defer st.Close()
		ctx := context.Background()
		if err := st.DeleteRun(ctx, "source"); err != nil {
			t.Fatal(err)
		}
		createRetryNodeFromSnapshot(t, st, retryID)
		node, err := st.GetNode(ctx, retryID, "deploy")
		if err != nil {
			t.Fatal(err)
		}
		record := mustNodeRecord(t, st, retryID, "deploy")
		if !nodeHasExecutionPolicy(record) || mustNodeExecutionPolicyPersistence(t, record).PolicyHash == "" || node.RetryRootRunID != "source" || node.AttemptsConsumed != 1 {
			t.Fatalf("retry continuity = %+v", node)
		}
		if node.AvoidCoordinatorID != "coordinator-old" || node.AvoidExecutorKind != "agent" || node.AvoidExecutorID != "executor-old" || node.AvoidUntil == nil {
			t.Fatalf("retry avoidance = %+v", node)
		}
		widened := policy
		widened.SecretNames = append(widened.SecretNames, "EXTRA_TOKEN")
		if err := st.markNodeReadyWithExecutionPolicy(ctx, retryID, "deploy", widened); !errors.Is(err, errExecutionPolicyConflict) {
			t.Fatalf("widened retry policy = %v, want conflict", err)
		}
		if err := st.markNodeReadyWithExecutionPolicy(ctx, retryID, "deploy", policy); err != nil {
			t.Fatalf("exact retry policy: %v", err)
		}
	})

	t.Run("missing or corrupt snapshot fails closed", func(t *testing.T) {
		for name, corrupt := range map[string]string{
			"missing": `DELETE FROM agent_loss_retry_node_sources WHERE retry_run_id = ? AND node_id = 'deploy'`,
			"corrupt": `UPDATE agent_loss_retry_node_sources SET policy_hash = 'sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff' WHERE retry_run_id = ? AND node_id = 'deploy'`,
		} {
			t.Run(name, func(t *testing.T) {
				st, retryID, _ := createSealedAgentLossRetry(t)
				defer st.Close()
				if _, err := st.DB().Exec(corrupt, retryID); err != nil {
					t.Fatal(err)
				}
				if err := st.CreateNode(context.Background(), retryNodeFromSnapshot(retryID)); !errors.Is(err, errExecutionPolicyInvalid) {
					t.Fatalf("CreateNode with %s snapshot = %v, want invalid", name, err)
				}
			})
		}
	})
}

func TestAgentLossRetryMergeKeepsFirstFrozenNodeSnapshot(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	plan := []byte(`{"nodes":[{"id":"a","modifiers":{"retry":1}},{"id":"b","modifiers":{"retry":1}}]}`)
	if err := st.CreateRun(ctx, Run{
		ID: "source", Pipeline: "release", Status: "running", TriggerSource: "manual",
		RepoURL: "https://example.invalid/repo.git", GitSHA: strings.Repeat("d", 40), PlanSnapshot: plan,
	}); err != nil {
		t.Fatal(err)
	}
	policies := map[string]nodeExecutionPolicy{}
	for _, nodeID := range []string{"a", "b"} {
		node := retryNodeFromSnapshot("source")
		node.NodeID = nodeID
		node.RequiredCoordinatorID = "coordinator-home"
		node.RequiredExecutorLocation = "cloud"
		if err := st.CreateNode(ctx, node); err != nil {
			t.Fatal(err)
		}
		policy := testExecutionPolicy()
		policy.NodeID = nodeID
		if err := st.markNodeReadyWithExecutionPolicy(ctx, "source", nodeID, policy); err != nil {
			t.Fatal(err)
		}
		policies[nodeID] = policy
	}

	firstTx, err := st.beginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	retryID, _, _, err := st.createAgentLossRetryTx(ctx, firstTx, "source", []expiredAgentNode{{
		runID: "source", nodeID: "a", requiredCoordinatorID: "coordinator-home", requiredLocation: "cloud",
	}}, time.Now())
	if err != nil {
		_ = firstTx.Rollback()
		t.Fatal(err)
	}
	if err := firstTx.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := st.DB().Exec(`UPDATE nodes SET execution_policy_hash = 'sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'
		WHERE run_id = 'source' AND node_id = 'b'`); err != nil {
		t.Fatal(err)
	}
	secondTx, err := st.beginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mergedID, _, _, err := st.createAgentLossRetryTx(ctx, secondTx, "source", []expiredAgentNode{{
		runID: "source", nodeID: "b", requiredCoordinatorID: "coordinator-home", requiredLocation: "cloud",
	}}, time.Now())
	if err != nil {
		_ = secondTx.Rollback()
		t.Fatal(err)
	}
	if mergedID != retryID {
		_ = secondTx.Rollback()
		t.Fatalf("merged retry = %q, want %q", mergedID, retryID)
	}
	if err := secondTx.Commit(); err != nil {
		t.Fatal(err)
	}

	want := mustSealExecutionPolicy(t, policies["b"]).Hash
	var got string
	if err := st.DB().QueryRow(`SELECT policy_hash FROM agent_loss_retry_node_sources WHERE retry_run_id = ? AND node_id = 'b'`, retryID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("merged snapshot hash = %q, want first frozen %q", got, want)
	}
}

func mustSealExecutionPolicy(t *testing.T, policy nodeExecutionPolicy) sealedExecutionPolicy {
	t.Helper()
	sealed, err := sealExecutionPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
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

func createSealedAgentLossRetry(t *testing.T) (*Store, string, nodeExecutionPolicy) {
	t.Helper()
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	plan := []byte(`{"nodes":[{"id":"deploy","modifiers":{"retry":1}}]}`)
	if err := st.CreateRun(ctx, Run{
		ID: "source", Pipeline: "release", Status: "running", TriggerSource: "manual",
		RepoURL: "https://example.invalid/repo.git", GitSHA: strings.Repeat("d", 40), PlanSnapshot: plan,
	}); err != nil {
		st.Close()
		t.Fatal(err)
	}
	sourceNode := retryNodeFromSnapshot("source")
	sourceNode.RequiredCoordinatorID = "coordinator-home"
	sourceNode.RequiredExecutorLocation = "cloud"
	if err := st.CreateNode(ctx, sourceNode); err != nil {
		st.Close()
		t.Fatal(err)
	}
	policy := testExecutionPolicy()
	if err := st.markNodeReadyWithExecutionPolicy(ctx, "source", "deploy", policy); err != nil {
		st.Close()
		t.Fatal(err)
	}
	avoidUntil := time.Now().Add(time.Minute).UnixNano()
	if _, err := st.DB().Exec(`UPDATE nodes SET attempts_consumed = 1,
		avoid_coordinator_id = 'coordinator-old', avoid_executor_kind = 'agent',
		avoid_executor_id = 'executor-old', avoid_until = ?
		WHERE run_id = 'source' AND node_id = 'deploy'`, avoidUntil); err != nil {
		st.Close()
		t.Fatal(err)
	}
	tx, err := st.beginTx(ctx)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	retryID, causes, _, err := st.createAgentLossRetryTx(ctx, tx, "source", []expiredAgentNode{{
		runID: "source", nodeID: "deploy", coordinatorID: "coordinator-old", executorKind: "agent",
		executorID: "executor-old", requiredCoordinatorID: "coordinator-home", requiredLocation: "cloud",
		started: true, invocations: 1,
	}}, time.Now())
	if err != nil {
		_ = tx.Rollback()
		st.Close()
		t.Fatal(err)
	}
	if retryID == "" || !slices.Equal(causes, []string{"deploy"}) {
		_ = tx.Rollback()
		st.Close()
		t.Fatalf("retry = %q causes=%v", retryID, causes)
	}
	if err := tx.Commit(); err != nil {
		st.Close()
		t.Fatal(err)
	}
	return st, retryID, policy
}

func retryNodeFromSnapshot(runID string) Node {
	return Node{
		RunID: runID, NodeID: "deploy", Status: "pending", Deps: []string{"build"},
		NeedsLabels: []string{"location=cloud", "gpu"}, PrefersLabels: []string{"fast"},
		RequestedCores: 2, RequestedMemoryBytes: 1 << 30, RequestedSlots: 1,
	}
}

func createRetryNodeFromSnapshot(t *testing.T, st *Store, retryID string) {
	t.Helper()
	if err := st.CreateNode(context.Background(), retryNodeFromSnapshot(retryID)); err != nil {
		t.Fatal(err)
	}
}
