package bincache

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const cacheQueueStateVersion = 1

type cacheQueueState struct {
	Version uint64 `json:"version"`
	Head    uint64 `json:"head"`
	Next    uint64 `json:"next"`
}

type queuedCacheCandidate struct {
	cacheCandidate
	sequence uint64
	missing  bool
}

// These seams define the persistent queue contract without changing cache
// behavior. The following regression commit pins the required semantics.
func enqueueCacheEntry(context.Context, string, string) (uint64, error) { return 0, nil }

func markCacheQueueRecordCurrent(string, uint64, time.Time) error { return nil }

func cacheQueueBatch(ctx context.Context, root string, limit int) ([]queuedCacheCandidate, bool, error) {
	candidates, exhausted, err := cacheCandidatesBounded(ctx, root, limit)
	if err != nil {
		return nil, false, err
	}
	queued := make([]queuedCacheCandidate, 0, len(candidates))
	for sequence, candidate := range candidates {
		queued = append(queued, queuedCacheCandidate{cacheCandidate: candidate, sequence: uint64(sequence)})
	}
	return queued, exhausted, nil
}

func consumeCacheQueueRecord(context.Context, string, uint64) error { return nil }

func readCacheQueueState(string) (cacheQueueState, error) {
	return cacheQueueState{Version: cacheQueueStateVersion}, nil
}

func cacheQueueRecordPath(root string, sequence uint64) string {
	return filepath.Join(root, "index", "records", fmt.Sprintf("%020d", sequence))
}

func writeAtomicFile(path string, body []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(body); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceCacheMetadata(tempPath, path)
}
