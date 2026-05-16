package sparkwing

import "github.com/sparkwing-dev/sparkwing/pkg/storage"

// Cache is the content-addressed artifact store. It also holds
// compiled pipeline binaries under bin/<hash>. Backend selection
// lives in .sparkwing/backends.yaml (see pkg/backends).
//
// Cache is an alias for storage.ArtifactStore so existing consumers
// of the storage package keep working unchanged.
type Cache = storage.ArtifactStore

// Logs is the per-job log stream store. Implementations buffer log
// bytes keyed by (runID, nodeID). Backend selection lives in
// .sparkwing/backends.yaml.
//
// Logs is an alias for storage.LogStore.
type Logs = storage.LogStore
