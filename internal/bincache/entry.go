package bincache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

const pipelineCacheSchema = "v1"

var errCacheAuthorityUnavailable = errors.New("pipeline cache authority unavailable")

// Entry is the authority to inspect, materialize, and execute one managed
// pipeline-cache entry.
type Entry struct {
	root string
	key  string
}

// Lease keeps an entry live until Release or process replacement.
type Lease struct {
	entry Entry
	file  *os.File
}

// PruneOptions bounds one cache-pressure reclamation attempt.
type PruneOptions struct {
	Root         string
	ReclaimBytes int64
	MaxEntries   int
}

// PruneResult reports observed pressure and work completed by Prune.
type PruneResult struct {
	ObservedBytes  int64 `json:"observed_bytes"`
	ReclaimedBytes int64 `json:"reclaimed_bytes"`
	Examined       int   `json:"examined_entries"`
	Reclaimed      int   `json:"reclaimed_entries"`
	Active         int   `json:"active_entries"`
	Busy           int   `json:"busy_entries"`
	Exhausted      bool  `json:"exhausted"`
}

// PipelineEntry resolves key inside Sparkwing's managed pipeline cache.
func PipelineEntry(key string) (Entry, error) {
	return pipelineEntryAt(filepath.Join(SparkwingHome(), "cache", "pipelines", pipelineCacheSchema), key)
}

func pipelineEntryAt(root, key string) (Entry, error) {
	return Entry{root: root, key: key}, nil
}

// Path returns the executable protected by the lease.
func (l *Lease) Path() string {
	if l == nil {
		return ""
	}
	return l.entry.binaryPath()
}

// Release relinquishes the entry lease.
func (l *Lease) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Acquire obtains a live-entry lease if the entry exists.
func (e Entry) Acquire(context.Context) (*Lease, bool, error) {
	return nil, false, errCacheAuthorityUnavailable
}

// Materialize publishes the entry through a private staging path.
func (e Entry) Materialize(context.Context, func(string) error) (bool, error) {
	return false, errCacheAuthorityUnavailable
}

// Prune reclaims inactive entries within the requested bounds.
func Prune(context.Context, PruneOptions) (PruneResult, error) {
	return PruneResult{}, errCacheAuthorityUnavailable
}

func (e Entry) binaryPath() string {
	name := "pipelines"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(e.root, "entries", e.key, name)
}
