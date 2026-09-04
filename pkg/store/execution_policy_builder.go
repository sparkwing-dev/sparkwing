package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/executionpolicy"
	"github.com/sparkwing-dev/sparkwing/internal/sourceidentity"
)

const (
	assistedDependencyMaxBytes       = 1 << 20
	assistedArtifactMaxBytes         = 64 << 20
	assistedArtifactMaxFiles         = 10_000
	assistedArtifactManifestMaxBytes = 1 << 20
	assistedResultMaxBytes           = 1 << 20
)

type executionPolicyPlan struct {
	Pipeline string                    `json:"pipeline"`
	RunID    string                    `json:"run_id"`
	Nodes    []executionPolicyPlanNode `json:"nodes"`
}

type executionPolicyPlanNode struct {
	ID           string                       `json:"id"`
	Deps         []string                     `json:"deps"`
	OptionalDeps []string                     `json:"optional_deps,omitempty"`
	Dynamic      bool                         `json:"dynamic,omitempty"`
	Outputs      []string                     `json:"outputs,omitempty"`
	Consumes     []executionPolicyPlanConsume `json:"consumes,omitempty"`
	Work         *executionPolicyPlanWork     `json:"work,omitempty"`
}

type executionPolicyPlanConsume struct {
	Producer string `json:"producer"`
	Into     string `json:"into,omitempty"`
}

type executionPolicyPlanWork struct {
	Steps []struct {
		ID string `json:"id"`
	} `json:"steps,omitempty"`
	Spawns    []json.RawMessage `json:"spawns,omitempty"`
	SpawnEach []json.RawMessage `json:"spawn_each,omitempty"`
}

func (s *Store) buildNodeExecutionPolicyTx(ctx context.Context, tx *storeTx, node *nodeRecord, pipeline string) (nodeExecutionPolicy, bool, error) {
	if node.RequiredCoordinatorID != "" || coordinatorOnlyNeeds(node.NeedsLabels) {
		return nodeExecutionPolicy{}, false, nil
	}
	allowed, err := executionpolicy.AllowedLocationsForBinding(executionpolicy.Binding{
		Pipeline: pipeline, NodeID: node.NodeID, Dependencies: node.Deps,
		NeedsLabels: node.NeedsLabels, RequiredExecutorLocation: node.RequiredExecutorLocation,
	})
	if err != nil {
		return nodeExecutionPolicy{}, false, err
	}

	var gitSHA string
	var invocationJSON, planJSON []byte
	var planHash string
	if err := tx.QueryRowContext(ctx, `
SELECT r.git_sha, r.invocation_json, r.plan_json, p.plan_hash
  FROM runs r JOIN run_definition_plans p ON p.run_id = r.id
 WHERE r.id = ? AND r.pipeline = ?`, node.RunID, pipeline).Scan(&gitSHA, &invocationJSON, &planJSON, &planHash); errors.Is(err, sql.ErrNoRows) {
		return nodeExecutionPolicy{}, false, nil
	} else if err != nil {
		return nodeExecutionPolicy{}, false, err
	}
	source, ok, err := fleetSourceFromInvocation(invocationJSON)
	if err != nil {
		return nodeExecutionPolicy{}, false, err
	}
	if !ok {
		return nodeExecutionPolicy{}, false, nil
	}
	if source.Kind != "working_tree" || source.Identity != gitSHA || !validLowerGitRevision(gitSHA) ||
		!sourceidentity.IsSHA256(source.ManifestDigest) {
		return nodeExecutionPolicy{}, false, fmt.Errorf("%w: Fleet source identity is not immutable", errExecutionPolicyInvalid)
	}
	planDigest := sha256.Sum256(planJSON)
	if got := fmt.Sprintf("sha256:%x", planDigest[:]); got != planHash {
		return nodeExecutionPolicy{}, false, fmt.Errorf("%w: stored plan no longer matches its definition hash", errExecutionPolicyInvalid)
	}
	var plan executionPolicyPlan
	if err := json.Unmarshal(planJSON, &plan); err != nil {
		return nodeExecutionPolicy{}, false, fmt.Errorf("%w: stored plan: %v", errExecutionPolicyInvalid, err)
	}
	if plan.Pipeline != pipeline || plan.RunID != node.RunID {
		return nodeExecutionPolicy{}, false, fmt.Errorf("%w: stored plan identity mismatch", errExecutionPolicyInvalid)
	}
	planned, ok := findExecutionPolicyPlanNode(plan.Nodes, node.NodeID)
	if !ok || planned.Dynamic || planned.Work == nil || len(planned.Work.Spawns) != 0 || len(planned.Work.SpawnEach) != 0 {
		return nodeExecutionPolicy{}, false, nil
	}
	plannedDeps := append(append([]string(nil), planned.Deps...), planned.OptionalDeps...)
	if !sameExactIdentities(plannedDeps, node.Deps) {
		return nodeExecutionPolicy{}, false, fmt.Errorf("%w: stored node dependencies differ from the immutable plan", errExecutionPolicyInvalid)
	}

	dependencies := make([]executionpolicy.NodeDependencyAuthority, len(node.Deps))
	for i, dependency := range node.Deps {
		dependencies[i] = executionpolicy.NodeDependencyAuthority{
			NodeID: dependency, WholeOutput: true, MaxBytes: assistedDependencyMaxBytes,
		}
	}
	artifactInputs := make([]executionpolicy.NodeArtifactInput, 0, len(planned.Consumes))
	for _, consume := range planned.Consumes {
		var manifest string
		if err := tx.QueryRowContext(ctx, `SELECT artifact_manifest FROM nodes WHERE run_id = ? AND node_id = ?`,
			node.RunID, consume.Producer).Scan(&manifest); errors.Is(err, sql.ErrNoRows) || manifest == "" {
			return nodeExecutionPolicy{}, false, nil
		} else if err != nil {
			return nodeExecutionPolicy{}, false, err
		}
		artifactInputs = append(artifactInputs, executionpolicy.NodeArtifactInput{
			ProducerNodeID: consume.Producer, ManifestDigest: manifest, Into: consume.Into,
			MaxBytes: assistedArtifactMaxBytes, MaxFiles: assistedArtifactMaxFiles,
			MaxManifestBytes: assistedArtifactManifestMaxBytes,
		})
	}
	artifactOutputs := make([]executionpolicy.NodeArtifactOutput, len(planned.Outputs))
	for i, glob := range planned.Outputs {
		artifactOutputs[i] = executionpolicy.NodeArtifactOutput{
			Glob: glob, MaxBytes: assistedArtifactMaxBytes, MaxFiles: assistedArtifactMaxFiles,
		}
	}
	stepIDs := make([]string, len(planned.Work.Steps))
	for i, step := range planned.Work.Steps {
		stepIDs[i] = step.ID
	}
	outputMax := uint64(0)
	if len(artifactOutputs) != 0 {
		outputMax = assistedArtifactMaxBytes
	}
	return nodeExecutionPolicy{
		Version: executionpolicy.NodeExecutionPolicyVersion, Pipeline: pipeline, NodeID: node.NodeID,
		AllowedLocations: allowed, Dependencies: dependencies,
		ArtifactInputs: artifactInputs, ArtifactOutputs: artifactOutputs,
		ArtifactOutputMaxBytes: outputMax, ResultMaxBytes: assistedResultMaxBytes,
		Emission:               executionpolicy.NodeEmissionAuthority{StepIDs: stepIDs},
		SupervisorRequirements: []string{executionpolicy.FleetSupervisorRequirement},
		Body: executionpolicy.NodeCompiledBodyAuthority{
			ProtocolVersion:     executionpolicy.AssistedBodyProtocolVersion,
			RuntimeRequirements: []string{executionpolicy.FleetBodyRequirement},
			Source: executionpolicy.NodeBodySourceAuthority{
				Kind: "working_tree", Identity: source.ManifestDigest,
				ManifestDigest: source.ManifestDigest, PlanDigest: planHash,
			},
		},
	}, true, nil
}

func coordinatorOnlyNeeds(needs []string) bool {
	for _, term := range needs {
		if term == "local" || term == "location=coordinator" {
			return true
		}
	}
	return false
}

type fleetInvocationSource struct {
	Kind           string `json:"kind"`
	Identity       string `json:"identity"`
	ManifestDigest string `json:"manifest_digest"`
}

func fleetSourceFromInvocation(raw []byte) (fleetInvocationSource, bool, error) {
	if len(raw) == 0 {
		return fleetInvocationSource{}, false, nil
	}
	var invocation map[string]json.RawMessage
	if err := decodeExecutionPolicyJSON(raw, &invocation); err != nil {
		return fleetInvocationSource{}, false, fmt.Errorf("%w: run invocation: %v", errExecutionPolicyInvalid, err)
	}
	sourceJSON, ok := invocation["fleet_source"]
	if !ok {
		return fleetInvocationSource{}, false, nil
	}
	var source fleetInvocationSource
	if err := decodeExecutionPolicyJSON(sourceJSON, &source); err != nil {
		return fleetInvocationSource{}, false, fmt.Errorf("%w: Fleet source: %v", errExecutionPolicyInvalid, err)
	}
	return source, true, nil
}

func decodeExecutionPolicyJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func findExecutionPolicyPlanNode(nodes []executionPolicyPlanNode, nodeID string) (executionPolicyPlanNode, bool) {
	for _, node := range nodes {
		if node.ID == nodeID {
			return node, true
		}
	}
	return executionPolicyPlanNode{}, false
}

func sameExactIdentities(left, right []string) bool {
	left, right = append([]string(nil), left...), append([]string(nil), right...)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right) && len(slices.Compact(left)) == len(left) && len(slices.Compact(right)) == len(right)
}

func validLowerGitRevision(value string) bool {
	if len(value) != 40 && len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
