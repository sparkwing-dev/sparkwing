package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const SubmissionEnvironmentCapturedKey = "_SPARKWING_SUBMISSION_ENV_CAPTURED"

const submissionEnvironmentDir = "submission-environments"

var submissionEnvironmentReconcileCursor atomic.Uint64

type submissionEnvironmentSnapshot struct {
	RunID       string   `json:"run_id"`
	Environment []string `json:"environment"`
}

func CaptureSubmissionEnvironment(home, runID string, env []string) error {
	layout, err := ConsumerLayoutFor(home)
	if err != nil {
		return err
	}
	dir := filepath.Join(layout.Home, submissionEnvironmentDir)
	if err := fssecure.EnsureDir(dir); err != nil {
		return fmt.Errorf("secure submission environment directory: %w", err)
	}
	body, err := json.Marshal(submissionEnvironmentSnapshot{RunID: runID, Environment: env})
	if err != nil {
		return fmt.Errorf("encode submission environment: %w", err)
	}
	path := submissionEnvironmentPath(layout.Home, runID)
	tmp, err := os.CreateTemp(dir, ".submission-environment-*")
	if err != nil {
		return fmt.Errorf("create submission environment: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write submission environment: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync submission environment: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close submission environment: %w", err)
	}
	if err := os.Link(tmpPath, path); err != nil {
		return fmt.Errorf("publish submission environment: %w", err)
	}
	return nil
}

func DiscardSubmissionEnvironment(home, runID string) error {
	layout, err := ConsumerLayoutFor(home)
	if err != nil {
		return err
	}
	err = os.Remove(submissionEnvironmentPath(layout.Home, runID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func submissionEnvironment(home string, trig *store.Trigger) ([]string, error) {
	if trig == nil || trig.TriggerEnv[SubmissionEnvironmentCapturedKey] != "1" {
		return nil, nil
	}
	body, err := os.ReadFile(submissionEnvironmentPath(home, trig.ID))
	if err != nil {
		return nil, fmt.Errorf("read submission environment: %w", err)
	}
	var snapshot submissionEnvironmentSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return nil, fmt.Errorf("decode submission environment: %w", err)
	}
	if snapshot.RunID != trig.ID {
		return nil, errors.New("submission environment run ID does not match trigger")
	}
	return snapshot.Environment, nil
}

func submissionEnvironmentPath(home, runID string) string {
	sum := sha256.Sum256([]byte(runID))
	return filepath.Join(home, submissionEnvironmentDir, hex.EncodeToString(sum[:])+".json")
}

func ReconcileSubmissionEnvironments(ctx context.Context, home string, st *store.Store, limit int) (int, error) {
	dir := filepath.Join(home, submissionEnvironmentDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	files := entries[:0]
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			files = append(files, entry)
		}
	}
	if len(files) == 0 {
		return 0, nil
	}
	count := len(files)
	if limit > 0 && count > limit {
		count = limit
	}
	start := int(submissionEnvironmentReconcileCursor.Add(uint64(count))-uint64(count)) % len(files)
	removed := 0
	for i := 0; i < count; i++ {
		entry := files[(start+i)%len(files)]
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return removed, readErr
		}
		var snapshot submissionEnvironmentSnapshot
		if jsonErr := json.Unmarshal(body, &snapshot); jsonErr != nil || snapshot.RunID == "" {
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return removed, removeErr
			}
			removed++
			continue
		}
		trig, getErr := st.GetTrigger(ctx, snapshot.RunID)
		terminal := errors.Is(getErr, store.ErrNotFound) || (getErr == nil && trig.Status == "done")
		if getErr != nil && !errors.Is(getErr, store.ErrNotFound) {
			return removed, getErr
		}
		if terminal {
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return removed, removeErr
			}
			removed++
		}
	}
	return removed, nil
}
