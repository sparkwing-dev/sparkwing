package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/executionpolicy"
)

func TestAssistedReadyBuildsPolicyOnlyFromPersistedControllerFacts(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	plan := `{"pipeline":"release","run_id":"run","nodes":[` +
		`{"id":"producer","deps":[],"work":{"steps":[{"id":"build"}]}},` +
		`{"id":"deploy","deps":["producer"],"outputs":["reports/report-*.json"],` +
		`"consumes":[{"producer":"producer","into":"artifacts/build"}],` +
		`"work":{"steps":[{"id":"publish"}]}}]}`
	manifest := "sha256:" + strings.Repeat("b", 64)
	createAssistedBuilderRun(t, st, "run", plan, manifest)
	if err := st.CreateNode(context.Background(), Node{RunID: "run", NodeID: "producer", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeArtifactManifest(context.Background(), "run", "producer", "sha256:"+strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(context.Background(), Node{
		RunID: "run", NodeID: "deploy", Status: "pending", Deps: []string{"producer"}, RequestedSlots: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(executionpolicy.WithAssistedReady(context.Background()), "run", "deploy"); err != nil {
		t.Fatal(err)
	}

	record := readNodeRecordForPolicyTest(t, st, "run", "deploy")
	policy, present, err := policyForNodeExecution(record)
	if err != nil || !present {
		t.Fatalf("built policy = (%+v, %v, %v)", policy, present, err)
	}
	if got, want := policy.AllowedLocations, []string{"cloud", "local"}; !sameExactIdentities(got, want) {
		t.Fatalf("allowed locations = %v, want %v", got, want)
	}
	if len(policy.SecretNames) != 0 || len(policy.Actions) != 0 {
		t.Fatalf("builder inferred opaque closure authority: secrets=%v actions=%v", policy.SecretNames, policy.Actions)
	}
	if len(policy.Dependencies) != 1 || policy.Dependencies[0].NodeID != "producer" ||
		!policy.Dependencies[0].WholeOutput || policy.Dependencies[0].MaxBytes != assistedDependencyMaxBytes {
		t.Fatalf("dependency authority = %+v", policy.Dependencies)
	}
	if len(policy.ArtifactInputs) != 1 || policy.ArtifactInputs[0].ManifestDigest != "sha256:"+strings.Repeat("c", 64) {
		t.Fatalf("artifact input authority = %+v", policy.ArtifactInputs)
	}
	if len(policy.ArtifactOutputs) != 1 || policy.ArtifactOutputs[0].Glob != "reports/report-*.json" {
		t.Fatalf("artifact output authority = %+v", policy.ArtifactOutputs)
	}
	if policy.Body.Source.ManifestDigest != manifest || policy.Body.Source.Identity != manifest {
		t.Fatalf("source authority = %+v", policy.Body.Source)
	}
	if !containsExact(policy.SupervisorRequirements, executionpolicy.FleetSupervisorRequirement) ||
		!containsExact(policy.Body.RuntimeRequirements, executionpolicy.FleetBodyRequirement) {
		t.Fatalf("runtime requirements = %v / %v", policy.SupervisorRequirements, policy.Body.RuntimeRequirements)
	}
}

func TestAssistedReadyLeavesUndeclaredDynamicAndCoordinatorOnlyNodesUnsealed(t *testing.T) {
	for name, nodePlan := range map[string]string{
		"dynamic": `{"id":"candidate","deps":[],"dynamic":true,"work":{"steps":[{"id":"opaque"}]}}`,
		"spawn":   `{"id":"candidate","deps":[],"work":{"spawns":[{"id":"undeclared"}]}}`,
		"local":   `{"id":"candidate","deps":[],"work":{"steps":[{"id":"local"}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			st := newTestStore(t)
			defer st.Close()
			plan := `{"pipeline":"release","run_id":"run","nodes":[` + nodePlan + `]}`
			createAssistedBuilderRun(t, st, "run", plan, "sha256:"+strings.Repeat("b", 64))
			node := Node{RunID: "run", NodeID: "candidate", Status: "pending"}
			if name == "local" {
				node.NeedsLabels = []string{"local"}
			}
			if err := st.CreateNode(context.Background(), node); err != nil {
				t.Fatal(err)
			}
			if err := st.MarkNodeReady(executionpolicy.WithAssistedReady(context.Background()), "run", "candidate"); err != nil {
				t.Fatal(err)
			}
			record := readNodeRecordForPolicyTest(t, st, "run", "candidate")
			if nodeHasExecutionPolicy(record) {
				t.Fatal("coordinator-only node received assisted authority")
			}
			if record.ReadyAt == nil {
				t.Fatal("coordinator fallback node was not made ready")
			}
		})
	}
}

func TestAssistedReadyRejectsTamperedPersistedPlanAndDependencyFacts(t *testing.T) {
	for name, mutate := range map[string]func(*Store){
		"plan": func(st *Store) {
			if _, err := st.exec(context.Background(), `UPDATE runs SET plan_json = ? WHERE id = ?`,
				[]byte(`{"pipeline":"release","run_id":"run","nodes":[]}`), "run"); err != nil {
				t.Fatal(err)
			}
		},
		"deps": func(st *Store) {},
	} {
		t.Run(name, func(t *testing.T) {
			st := newTestStore(t)
			defer st.Close()
			plan := `{"pipeline":"release","run_id":"run","nodes":[{"id":"candidate","deps":["producer"],"work":{"steps":[{"id":"run"}]}}]}`
			createAssistedBuilderRun(t, st, "run", plan, "sha256:"+strings.Repeat("b", 64))
			deps := []string{"producer"}
			if name == "deps" {
				deps = []string{"attacker"}
			}
			if err := st.CreateNode(context.Background(), Node{RunID: "run", NodeID: "candidate", Status: "pending", Deps: deps}); err != nil {
				t.Fatal(err)
			}
			mutate(st)
			err := st.MarkNodeReady(executionpolicy.WithAssistedReady(context.Background()), "run", "candidate")
			if !errors.Is(err, executionpolicy.ErrExecutionPolicyInvalid) {
				t.Fatalf("tampered %s readiness error = %v, want invalid", name, err)
			}
			record := readNodeRecordForPolicyTest(t, st, "run", "candidate")
			if record.ReadyAt != nil || nodeHasExecutionPolicy(record) {
				t.Fatalf("tampered %s transition was not atomic", name)
			}
		})
	}
}

func createAssistedBuilderRun(t *testing.T, st *Store, runID, plan, manifest string) {
	t.Helper()
	gitSHA := strings.Repeat("a", 40)
	if err := st.CreateRun(context.Background(), Run{
		ID: runID, Pipeline: "release", Status: "running", GitSHA: gitSHA, StartedAt: time.Now(),
		Invocation: map[string]any{"fleet_source": map[string]any{
			"kind": "working_tree", "identity": gitSHA, "manifest_digest": manifest,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdatePlanSnapshot(context.Background(), runID, []byte(plan)); err != nil {
		t.Fatal(err)
	}
}

func readNodeRecordForPolicyTest(t *testing.T, st *Store, runID, nodeID string) *nodeRecord {
	t.Helper()
	record := &nodeRecord{}
	if err := scanNodeRow(st.queryRow(context.Background(), `SELECT `+nodeSelectColumns+`
FROM nodes WHERE run_id = ? AND node_id = ?`, runID, nodeID), record); err != nil {
		t.Fatal(err)
	}
	return record
}
