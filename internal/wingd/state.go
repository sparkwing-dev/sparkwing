package wingd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
)

const stateSchema = 1

// safety: schema 2 was written only while an admission was parked on a process
// session; refusing it would stop the successor of such a daemon from starting.
const legacyGuardedStateSchema = 2

type persistedState struct {
	Schema        int                `json:"schema"`
	Snapshot      admission.Snapshot `json:"snapshot"`
	Events        []admissionEvent   `json:"events,omitempty"`
	CancelledRuns []string           `json:"cancelled_runs,omitempty"`
}

func writeState(path string, snap admission.Snapshot, events []admissionEvent) error {
	return writeStateWithCancellations(path, snap, events, nil)
}

func writeStateWithCancellations(path string, snap admission.Snapshot, events []admissionEvent, cancelledRuns []string) error {
	snap.Waiters = nil
	data, err := json.Marshal(persistedState{Schema: stateSchema, Snapshot: snap, Events: events, CancelledRuns: cancelledRuns})
	if err != nil {
		return fmt.Errorf("wingd: marshal state: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("wingd: temp state: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("wingd: write state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("wingd: sync state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("wingd: close state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("wingd: rename state: %w", err)
	}
	if err := syncStateDirectory(dir); err != nil {
		return err
	}
	return nil
}

func quarantineState(path string, now time.Time) (string, error) {
	dst := fmt.Sprintf("%s.corrupt-%d", path, now.Unix())
	if err := os.Rename(path, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func readState(path string) (*admission.Snapshot, []admissionEvent, error) {
	snap, events, _, err := readStateWithCancellations(path)
	return snap, events, err
}

func readStateWithCancellations(path string) (*admission.Snapshot, []admissionEvent, []string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("wingd: read state: %w", err)
	}
	var st persistedState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, nil, nil, fmt.Errorf("wingd: parse state: %w", err)
	}
	if st.Schema != stateSchema && st.Schema != legacyGuardedStateSchema {
		return nil, nil, nil, fmt.Errorf("wingd: state schema %d, want %d or %d", st.Schema, stateSchema, legacyGuardedStateSchema)
	}
	return &st.Snapshot, st.Events, st.CancelledRuns, nil
}
