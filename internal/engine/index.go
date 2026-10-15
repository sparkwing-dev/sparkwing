package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// Index refreshes signed policy and retains verified fallback without an age cutoff.
func (r *Resolver) Index(ctx context.Context, now time.Time) (IndexResult, error) {
	r.indexMu.Lock()
	defer r.indexMu.Unlock()
	if err := ctx.Err(); err != nil {
		return IndexResult{}, err
	}
	if r.index.index.Format == 0 {
		cached, err := r.loadIndex(now)
		if err != nil {
			return IndexResult{}, err
		}
		r.index = cached
	}
	if !r.checkedAt.IsZero() && !now.Before(r.checkedAt) && now.Before(r.checkedAt.Add(limits.VersionIndexRefresh)) {
		return indexResult(r.index, r.indexErr), nil
	}
	candidate, err := r.fetchIndex(ctx, now)
	if err == nil {
		err = checkPolicyAdvance(r.index, candidate)
	}
	if err == nil {
		err = r.saveIndex(candidate)
	}
	if err == nil {
		r.index = candidate
		r.checkedAt = now
		r.indexErr = nil
		return IndexResult{Index: candidate}, nil
	}
	if ctx.Err() != nil {
		return IndexResult{}, ctx.Err()
	}
	// safety: Another process may have persisted a revocation while this resolver was fetching.
	persisted, cacheErr := r.loadIndex(now)
	if cacheErr == nil && persisted.index.Format != 0 && !persisted.index.PublishedAt.Before(r.index.index.PublishedAt) {
		cacheErr = checkPolicyAdvance(r.index, persisted)
		if cacheErr == nil {
			r.index = persisted
		}
	}
	err = errors.Join(err, cacheErr)
	if r.index.index.Format == 0 {
		return IndexResult{}, &apierr.Error{Code: apierr.Unavailable, Message: fmt.Sprintf("Cannot obtain a verified version policy: %v", err), Hint: "Check connectivity to sparkwing.dev and retry before running the pipeline."}
	}
	r.checkedAt = now
	r.indexErr = err
	return indexResult(r.index, err), nil
}

func (r *Resolver) fetchIndex(ctx context.Context, now time.Time) (VerifiedIndex, error) {
	body, err := r.fetch(ctx, VersionIndexURL, false)
	if err != nil {
		return VerifiedIndex{}, err
	}
	signature, err := r.fetch(ctx, VersionIndexSignatureURL, false)
	if err != nil {
		return VerifiedIndex{}, err
	}
	return VerifyIndex(SignedIndex{Body: body, Signature: signature}, now)
}

func checkPolicyAdvance(previous, next VerifiedIndex) error {
	if previous.index.Format == 0 {
		return nil
	}
	before, after := previous.index.PublishedAt, next.index.PublishedAt
	if after.Before(before) || (after.Equal(before) && !bytes.Equal(previous.signed.Body, next.signed.Body)) {
		return fmt.Errorf("refused version policy rollback or changed body at the same publication time")
	}
	records := map[string]VersionInfo{}
	for _, record := range next.index.Versions {
		records[record.Version] = record
	}
	for _, record := range previous.index.Versions {
		if current, ok := records[record.Version]; !ok || current != record {
			return fmt.Errorf("version index changed or removed a retained release record")
		}
	}
	return nil
}

func (r *Resolver) loadIndex(now time.Time) (VerifiedIndex, error) {
	dir := filepath.Join(r.config.CacheDirectory, "policy")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return VerifiedIndex{}, nil
	}
	if err != nil {
		return VerifiedIndex{}, err
	}
	var latest VerifiedIndex
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return VerifiedIndex{}, err
		}
		var signed SignedIndex
		if err := json.Unmarshal(body, &signed); err != nil {
			continue
		}
		candidate, err := VerifyIndex(signed, now)
		if err != nil {
			continue
		}
		if latest.index.Format != 0 && candidate.index.PublishedAt.Before(latest.index.PublishedAt) {
			continue
		}
		if err := checkPolicyAdvance(latest, candidate); err != nil {
			return VerifiedIndex{}, integrity(err)
		}
		latest = candidate
	}
	return latest, nil
}

func (r *Resolver) saveIndex(candidate VerifiedIndex) error {
	// safety: Immutable snapshots keep concurrent processes from overwriting a newer policy floor.
	dir := filepath.Join(r.config.CacheDirectory, "policy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	previous, err := r.loadIndex(candidate.verifiedAt)
	if err != nil {
		return err
	}
	if err := checkPolicyAdvance(previous, candidate); err != nil {
		return err
	}
	body, err := json.Marshal(candidate.signed)
	if err != nil {
		return err
	}
	name := candidate.index.PublishedAt.UTC().Format("20060102T150405.000000000") + "-" + digest(candidate.signed.Body) + ".json"
	return atomicWrite(filepath.Join(dir, name), body, 0o600)
}

func atomicWrite(path string, body []byte, mode os.FileMode) (result error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil && !os.IsNotExist(err) && result == nil {
			result = err
		}
	}()
	if err := file.Chmod(mode); err != nil {
		closeErr := file.Close()
		return fmt.Errorf("set staged mode: %w (close: %v)", err, closeErr)
	}
	if _, err := file.Write(body); err != nil {
		closeErr := file.Close()
		return fmt.Errorf("write staged file: %w (close: %v)", err, closeErr)
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func indexResult(index VerifiedIndex, err error) IndexResult {
	result := IndexResult{Index: index}
	if err != nil {
		result.Fallback = true
		result.Notices = []Notice{{Kind: NoticeIndexFallback, Reason: fmt.Sprintf("Using the last verified version policy: %v", err), Hint: "Check connectivity to sparkwing.dev and retry."}}
	}
	return result
}
