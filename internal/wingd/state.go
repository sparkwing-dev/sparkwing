package wingd

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

const stateSchema = 1

type persistedState struct {
	Schema        int                `json:"schema"`
	Snapshot      admission.Snapshot `json:"snapshot"`
	Events        []admissionEvent   `json:"events,omitempty"`
	CancelledRuns []string           `json:"cancelled_runs,omitempty"`
}

func writeStateWithCancellations(path string, snap admission.Snapshot, events []admissionEvent, cancelledRuns []string) error {
	snap.Waiters = nil
	data, err := json.Marshal(persistedState{Schema: stateSchema, Snapshot: snap, Events: events, CancelledRuns: cancelledRuns})
	if err != nil {
		return fmt.Errorf("wingd: marshal state: %w", err)
	}
	return writeAtomicFile(path, data)
}

func writeAtomicFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := fssecure.OpenFile(filepath.Join(dir, "."+filepath.Base(path)+"-"+rand.Text()+".tmp"), os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return fmt.Errorf("wingd: temp %s: %w", path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("wingd: write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("wingd: sync %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("wingd: close %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("wingd: rename %s: %w", path, err)
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
	if st.Schema != stateSchema {
		return nil, nil, nil, fmt.Errorf("wingd: state schema %d, want %d", st.Schema, stateSchema)
	}
	return &st.Snapshot, st.Events, st.CancelledRuns, nil
}
