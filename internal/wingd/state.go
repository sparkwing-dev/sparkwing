package wingd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const stateSchema = 2

type persistedGuard struct {
	LeaseID admission.LeaseID       `json:"lease_id"`
	RunID   string                  `json:"run_id"`
	Session wingwire.ProcessSession `json:"session"`
}

type persistedState struct {
	Schema        int                `json:"schema"`
	Snapshot      admission.Snapshot `json:"snapshot"`
	Events        []admissionEvent   `json:"events,omitempty"`
	CancelledRuns []string           `json:"cancelled_runs,omitempty"`
	Guards        []persistedGuard   `json:"guards,omitempty"`
}

func writeState(path string, snap admission.Snapshot, events []admissionEvent) error {
	return writeStateWithCancellations(path, snap, events, nil)
}

func writeStateWithCancellations(path string, snap admission.Snapshot, events []admissionEvent, cancelledRuns []string) error {
	return writeStateWithGuards(path, snap, events, cancelledRuns, nil)
}

func writeStateWithGuards(path string, snap admission.Snapshot, events []admissionEvent, cancelledRuns []string, guards []persistedGuard) error {
	snap.Waiters = nil
	schema := 1
	if len(guards) > 0 {
		schema = stateSchema
	}
	data, err := json.Marshal(persistedState{Schema: schema, Snapshot: snap, Events: events, CancelledRuns: cancelledRuns, Guards: guards})
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
	snap, events, cancelledRuns, _, err := readStateWithGuards(path)
	return snap, events, cancelledRuns, err
}

func readStateWithGuards(path string) (*admission.Snapshot, []admissionEvent, []string, []persistedGuard, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("wingd: read state: %w", err)
	}
	var st persistedState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("wingd: parse state: %w", err)
	}
	if st.Schema != 1 && st.Schema != stateSchema {
		return nil, nil, nil, nil, fmt.Errorf("wingd: state schema %d, want 1 or %d", st.Schema, stateSchema)
	}
	return &st.Snapshot, st.Events, st.CancelledRuns, st.Guards, nil
}
