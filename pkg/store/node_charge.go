package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// ExecutorResource is a CPU and memory capacity or charge.
type ExecutorResource = match.Resources

func nodeChargeTx(ctx context.Context, q rowQuerier, runID, nodeID string) (ExecutorResource, error) {
	var team, pipeline string
	var plan []byte
	if err := q.QueryRowContext(ctx,
		`SELECT team, pipeline, plan_json FROM runs WHERE id = ?`, runID,
	).Scan(&team, &pipeline, &plan); err != nil {
		return ExecutorResource{}, err
	}
	if pin := snapshotNodeResource(plan, nodeID); pin.Cores > 0 || pin.MemoryBytes > 0 {
		return pin, nil
	}
	row := q.QueryRowContext(ctx, `
SELECT `+profileColumns+`
  FROM pipeline_profiles WHERE team = ? AND pipeline = ? AND node_id = ?`, team, pipeline, nodeID)
	profile, err := scanProfile(row, pipeline, nodeID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ExecutorResource{}, err
	}
	if err == nil {
		return nodeChargeFromSnapshot(plan, nodeID, profile), nil
	}
	return nodeChargeFromSnapshot(plan, nodeID, nil), nil
}

func nodeChargeFromSnapshot(plan []byte, nodeID string, profile *PipelineProfile) ExecutorResource {
	if pin := snapshotNodeResource(plan, nodeID); pin.Cores > 0 || pin.MemoryBytes > 0 {
		return pin
	}
	if profile != nil && (profile.PinnedCores > 0 || profile.PinnedMemoryBytes > 0) {
		return ExecutorResource{Cores: profile.PinnedCores, MemoryBytes: profile.PinnedMemoryBytes}
	}
	if profile != nil && profile.CPUMeasured {
		if profile.SampleCount >= 3 {
			cores := profile.SustainedCores
			if cores <= 0 {
				cores = profile.PeakCores
			}
			return ExecutorResource{Cores: math.Max(cores, 0.1), MemoryBytes: profile.PeakMemoryBytes}
		}
		cores := profile.PrevSustainedCores
		if cores <= 0 {
			cores = profile.PrevPeakCores
		}
		cores = math.Max(cores, 2*profile.FloorCores)
		floorMemory := int64(math.MaxInt64)
		if profile.FloorMemoryBytes <= math.MaxInt64/2 {
			floorMemory = 2 * profile.FloorMemoryBytes
		}
		memory := max(profile.PrevPeakMemoryBytes, floorMemory)
		if cores > 0 || memory > 0 {
			return ExecutorResource{Cores: cores, MemoryBytes: memory}
		}
	}
	return ExecutorResource{Cores: 1}
}

func snapshotNodeResource(raw []byte, nodeID string) ExecutorResource {
	var snapshot struct {
		Nodes []struct {
			ID        string `json:"id"`
			Modifiers *struct {
				Cores       float64 `json:"res_cores"`
				MemoryBytes int64   `json:"res_memory_bytes"`
			} `json:"modifiers"`
		} `json:"nodes"`
	}
	if json.Unmarshal(raw, &snapshot) != nil {
		return ExecutorResource{}
	}
	for _, node := range snapshot.Nodes {
		if node.ID == nodeID && node.Modifiers != nil {
			return ExecutorResource{Cores: node.Modifiers.Cores, MemoryBytes: node.Modifiers.MemoryBytes}
		}
	}
	return ExecutorResource{}
}

func snapshotNodePrefers(raw []byte, nodeID string) []string {
	var snapshot struct {
		Nodes []struct {
			ID        string `json:"id"`
			Modifiers *struct {
				Prefers []string `json:"prefers"`
			} `json:"modifiers"`
		} `json:"nodes"`
	}
	if json.Unmarshal(raw, &snapshot) != nil {
		return nil
	}
	for _, node := range snapshot.Nodes {
		if node.ID == nodeID && node.Modifiers != nil {
			return append([]string(nil), node.Modifiers.Prefers...)
		}
	}
	return nil
}

func snapshotRunPriority(raw []byte) int {
	var snapshot struct {
		Priority int `json:"priority"`
	}
	if json.Unmarshal(raw, &snapshot) != nil {
		return 0
	}
	return max(-100, min(100, snapshot.Priority))
}
