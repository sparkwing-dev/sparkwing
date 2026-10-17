package wingwire

import (
	"encoding/base64"
	"strings"
)

// RunID identifies a local or remote run independently of its admission holders.
type RunID string

// HolderRef identifies a run, node-host or semaphore admission participant.
type HolderRef string

// RemoteRunID identifies machine admission owned by another controller.
func RemoteRunID(controller, run string) RunID {
	encode := base64.RawURLEncoding.EncodeToString
	return RunID("remote/" + encode([]byte(strings.TrimRight(controller, "/"))) + "." + encode([]byte(run)))
}

// Remote reports whether the run is outside the machine controller's store.
func (run RunID) Remote() bool {
	encoded, ok := strings.CutPrefix(string(run), "remote/")
	if !ok {
		return false
	}
	controller, id, ok := strings.Cut(encoded, ".")
	if !ok {
		return false
	}
	address, addressErr := base64.RawURLEncoding.DecodeString(controller)
	identity, identityErr := base64.RawURLEncoding.DecodeString(id)
	return addressErr == nil && identityErr == nil && len(address) > 0 && len(identity) > 0
}

// RunID resolves a holder reference to its run owner identity.
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
