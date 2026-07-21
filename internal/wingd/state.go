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

// stateSchema versions the on-disk state file so a future format change
// can be detected rather than misparsed.
const stateSchema = 1

// persistedState is the durable form of the ledger. Only granted leases
// survive a restart: waiters hold nothing and no re-attach token, so a
// queued run simply re-submits on reconnect instead of being restored
// into a lease nobody can claim. Events is the rolling admission-outcome
// window behind the queue view's health line; state files written before
// the window existed simply restore it empty.
type persistedState struct {
	Schema   int                `json:"schema"`
	Snapshot admission.Snapshot `json:"snapshot"`
	Events   []admissionEvent   `json:"events,omitempty"`
}

// writeState writes snap and the event window to path by atomic rename,
// stripping waiters so a restored ledger contains only reclaimable
// leases.
func writeState(path string, snap admission.Snapshot, events []admissionEvent) error {
	snap.Waiters = nil
	data, err := json.Marshal(persistedState{Schema: stateSchema, Snapshot: snap, Events: events})
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
	return nil
}

// quarantineState moves an unusable state file aside under a
// .corrupt-<unixtime> suffix so the next start does not reread it, and
// returns the quarantine path. The original bytes are preserved for
// forensics; sparkwing doctor reports quarantined files.
func quarantineState(path string, now time.Time) (string, error) {
	dst := fmt.Sprintf("%s.corrupt-%d", path, now.Unix())
	if err := os.Rename(path, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// readState loads a persisted snapshot and event window. It returns
// (nil, nil, nil) when no state file exists, so a fresh daemon starts
// empty.
func readState(path string) (*admission.Snapshot, []admissionEvent, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("wingd: read state: %w", err)
	}
	var st persistedState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, nil, fmt.Errorf("wingd: parse state: %w", err)
	}
	if st.Schema != stateSchema {
		return nil, nil, fmt.Errorf("wingd: state schema %d, want %d", st.Schema, stateSchema)
	}
	return &st.Snapshot, st.Events, nil
}
