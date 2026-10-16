package wingwire

import (
	"encoding/base64"
	"strings"
)

// RunID identifies a persisted run independently of its admission holders.
type RunID string

// HolderRef identifies a run, node-host or semaphore admission participant.
type HolderRef string

// RunID resolves a holder reference to its persisted run identity.
func (ref HolderRef) RunID() RunID {
	for _, marker := range []string{"/node-host/", "/node-semaphore/"} {
		owner, encoded, found := strings.Cut(string(ref), marker)
		if found && owner != "" {
			if _, err := base64.RawURLEncoding.DecodeString(encoded); err == nil {
				return RunID(owner)
			}
		}
	}
	return RunID(strings.TrimSuffix(string(ref), "/plan"))
}
