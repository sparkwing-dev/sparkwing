package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const RunHandleSchemaVersion = 1

// RunHandle identifies one accepted run through every execution mode.
type RunHandle struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	Pipeline      string `json:"pipeline"`
	LogPath       string `json:"log_path,omitempty"`
	Status        string `json:"status,omitempty"`
}

func NewRunHandle(runID, pipeline, logPath, status string) RunHandle {
	return RunHandle{
		SchemaVersion: RunHandleSchemaVersion,
		RunID:         runID,
		Pipeline:      pipeline,
		LogPath:       logPath,
		Status:        status,
	}
}

func publishRunHandle(path string, handle RunHandle) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".sparkwing-run-handle-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := json.NewEncoder(tmp).Encode(handle); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		defer func() { _ = d.Close() }()
		if err := d.Sync(); err != nil {
			return fmt.Errorf("sync run-handle directory: %w", err)
		}
	}
	return nil
}
