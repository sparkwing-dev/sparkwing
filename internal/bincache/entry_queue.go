package bincache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const cacheQueueStateVersion = 1

const cacheQueueLockRetry = 10 * time.Millisecond

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

func enqueueCacheEntry(ctx context.Context, root, key string) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	lock, err := openCacheQueueLock(ctx, root)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = cacheUnlock(lock)
		_ = lock.Close()
	}()
	state, err := readCacheQueueState(root)
	if err != nil {
		return 0, err
	}
	if _, statErr := os.Stat(cacheQueueRecordPath(root, state.Next)); statErr == nil {
		if state.Next == ^uint64(0) {
			return 0, errors.New("pipeline cache queue sequence exhausted")
		}
		state.Next++
		if err := writeCacheQueueState(root, state); err != nil {
			return 0, err
		}
		if _, nextErr := os.Stat(cacheQueueRecordPath(root, state.Next)); nextErr == nil {
			return 0, errors.New("pipeline cache queue contains consecutive uncommitted records")
		} else if !errors.Is(nextErr, fs.ErrNotExist) {
			return 0, nextErr
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return 0, statErr
	}
	if state.Next == ^uint64(0) {
		return 0, errors.New("pipeline cache queue sequence exhausted")
	}
	sequence := state.Next
	recordPath := cacheQueueRecordPath(root, sequence)
	if err := writeAtomicFile(recordPath, []byte(key+"\n"), 0o600); err != nil {
		return 0, err
	}
	state.Next++
	if err := writeCacheQueueState(root, state); err != nil {
		return 0, err
	}
	return sequence, nil
}

func markCacheQueueRecordCurrent(root string, sequence uint64, usedAt time.Time) error {
	return os.Chtimes(cacheQueueRecordPath(root, sequence), usedAt, usedAt)
}

func cacheQueueBatch(ctx context.Context, root string, limit int) ([]queuedCacheCandidate, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if limit <= 0 {
		return nil, false, nil
	}
	lock, err := openCacheQueueLock(ctx, root)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		_ = cacheUnlock(lock)
		_ = lock.Close()
	}()
	state, err := readCacheQueueState(root)
	if err != nil {
		return nil, false, err
	}
	batch := make([]queuedCacheCandidate, 0, limit)
	for sequence := state.Head; sequence < state.Next && len(batch) < limit; sequence++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		path := cacheQueueRecordPath(root, sequence)
		body, readErr := os.ReadFile(path)
		if errors.Is(readErr, fs.ErrNotExist) {
			batch = append(batch, queuedCacheCandidate{sequence: sequence, missing: true})
			continue
		}
		if readErr != nil {
			return nil, false, readErr
		}
		key := string(body)
		if len(key) != 18 || key[17] != '\n' || !pipelineEntryKeyRE.MatchString(key[:17]) {
			return nil, false, fmt.Errorf("invalid pipeline cache queue record %d", sequence)
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil, false, statErr
		}
		batch = append(batch, queuedCacheCandidate{
			cacheCandidate: cacheCandidate{key: key[:17], modTime: info.ModTime().UnixNano()},
			sequence:       sequence,
		})
	}
	return batch, state.Head+uint64(len(batch)) < state.Next, nil
}

func consumeCacheQueueRecord(ctx context.Context, root string, sequence uint64) error {
	lock, err := openCacheQueueLock(ctx, root)
	if err != nil {
		return err
	}
	defer func() {
		_ = cacheUnlock(lock)
		_ = lock.Close()
	}()
	state, err := readCacheQueueState(root)
	if err != nil {
		return err
	}
	if sequence != state.Head {
		return fmt.Errorf("pipeline cache queue head is %d, cannot consume %d", state.Head, sequence)
	}
	recordPath := cacheQueueRecordPath(root, sequence)
	if err := os.Remove(recordPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := syncCacheMetadataDir(filepath.Dir(recordPath)); err != nil {
		return err
	}
	state.Head++
	return writeCacheQueueState(root, state)
}

func openCacheQueueLock(ctx context.Context, root string) (*os.File, error) {
	for {
		lock, acquired, err := openCacheLock(root, "entry-queue", cacheLockExclusiveNonblock)
		if err != nil {
			return nil, err
		}
		if acquired {
			return lock, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(cacheQueueLockRetry):
		}
	}
}

func readCacheQueueState(root string) (cacheQueueState, error) {
	path := filepath.Join(root, "index", "state.json")
	body, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cacheQueueState{Version: cacheQueueStateVersion}, nil
	}
	if err != nil {
		return cacheQueueState{}, err
	}
	var state cacheQueueState
	if err := json.Unmarshal(body, &state); err != nil {
		return cacheQueueState{}, fmt.Errorf("decode pipeline cache queue state: %w", err)
	}
	if state.Version != cacheQueueStateVersion || state.Head > state.Next {
		return cacheQueueState{}, errors.New("invalid pipeline cache queue state")
	}
	return state, nil
}

func writeCacheQueueState(root string, state cacheQueueState) error {
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeAtomicFile(filepath.Join(root, "index", "state.json"), body, 0o600)
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
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceCacheMetadata(tempPath, path); err != nil {
		return err
	}
	return syncCacheMetadataDir(filepath.Dir(path))
}
