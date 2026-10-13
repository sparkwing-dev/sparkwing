package localws

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// VersionInfo is the body of GET /api/v1/version: the running
// dashboard's own identity, used to bind readiness to one supervisor. The
// endpoint is unauthenticated by design -- it exposes no state, only
// the binary's own version and the schema it understands, which a
// starting CLI needs before it holds any credential.
type VersionInfo struct {
	Version  string `json:"version"`
	Schema   int    `json:"schema"`
	PID      int    `json:"pid"`
	Instance string `json:"instance,omitempty"`
}

func versionHandler(version string, instances ...string) http.HandlerFunc {
	instance := ""
	if len(instances) > 0 {
		instance = instances[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(VersionInfo{
			Version:  version,
			Schema:   store.ExpectedSchemaVersion(),
			PID:      os.Getpid(),
			Instance: instance,
		})
	}
}
