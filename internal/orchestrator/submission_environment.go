package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/envredact"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const SubmissionEnvironmentCapturedKey = "_SPARKWING_SUBMISSION_ENV_CAPTURED"

const submissionEnvironmentAllowKey = "SPARKWING_SUBMIT_ENV_ALLOW"

const submissionEnvironmentDir = "submission-environments"
const abandonedSubmissionEnvironmentAge = 10 * time.Minute

var submissionEnvironmentReconcileCursors sync.Map

type submissionEnvironmentSnapshot struct {
	RunID       string   `json:"run_id"`
	Environment []string `json:"environment"`
}

func CaptureSubmissionEnvironment(home, runID string, env []string, logger *slog.Logger) error {
	layout, err := ConsumerLayoutFor(home)
	if err != nil {
		return err
	}
	captured, err := filterSubmissionEnvironment(env, logger)
	if err != nil {
		return err
	}
	dir := filepath.Join(layout.Home, submissionEnvironmentDir)
	if err := fssecure.EnsureDir(dir); err != nil {
		return fmt.Errorf("secure submission environment directory: %w", err)
	}
	body, err := json.Marshal(submissionEnvironmentSnapshot{RunID: runID, Environment: captured})
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
	if err := os.Remove(tmpPath); err != nil {
		return fmt.Errorf("remove submission environment temporary file: %w", err)
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open submission environment directory: %w", err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync submission environment directory: %w", err)
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
	if errors.Is(err, os.ErrNotExist) {
		// safety: the snapshot dies at run start, so a redispatch fails closed rather than taking the consumer shell.
		return nil, errors.New("submission environment snapshot is gone; this run already started once, submit it again")
	}
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

func filterSubmissionEnvironment(env []string, logger *slog.Logger) ([]string, error) {
	names, prefixes, err := submissionEnvironmentAllowList(env)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(env))
	var dropped []string
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !submissionEnvironmentAllowed(key, names, prefixes) {
			continue
		}
		// safety: the snapshot outlives the shell, so a credential-shaped name or value never reaches it.
		if envredact.CredentialName(key) || envredact.CredentialValue(value) || envredact.RedactValue(value) != value {
			if names[key] {
				dropped = append(dropped, key)
			}
			continue
		}
		out = append(out, entry)
	}
	if len(dropped) > 0 && logger != nil {
		logger.Warn("submission environment: credential filter dropped allow-listed names",
			"names", strings.Join(dropped, ","), "allow_key", submissionEnvironmentAllowKey)
	}
	return out, nil
}

func submissionEnvironmentAllowList(env []string) (map[string]bool, []string, error) {
	names := map[string]bool{}
	var prefixes []string
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key != submissionEnvironmentAllowKey {
			continue
		}
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			switch {
			case item == "":
			case item == "*":
				return nil, nil, fmt.Errorf(
					"%s: %q is not a wildcard; name each variable or give a prefix such as AWS_*",
					submissionEnvironmentAllowKey, item)
			case strings.HasSuffix(item, "*"):
				prefixes = append(prefixes, strings.TrimSuffix(item, "*"))
			default:
				names[item] = true
			}
		}
	}
	return names, prefixes, nil
}

func submissionEnvironmentAllowed(key string, names map[string]bool, prefixes []string) bool {
	if envAllowed(key) || names[key] {
		return true
	}
	for _, prefix := range prefixes {
		if prefix != "" && strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func consumeSubmissionEnvironment(home string, trig *store.Trigger, logger *slog.Logger) ([]string, error) {
	env, err := submissionEnvironment(home, trig)
	// safety: the snapshot's life ends when the run starts, not when it finishes.
	if discardErr := DiscardSubmissionEnvironment(home, trig.ID); discardErr != nil {
		logger.Warn("discard submission environment", "trigger_id", trig.ID, "err", discardErr)
	}
	return env, err
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
		if !entry.IsDir() && (strings.HasSuffix(entry.Name(), ".json") || strings.HasPrefix(entry.Name(), ".submission-environment-")) {
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
	cursorValue, _ := submissionEnvironmentReconcileCursors.LoadOrStore(dir, &atomic.Uint64{})
	cursor := cursorValue.(*atomic.Uint64)
	start := int(cursor.Add(uint64(count))-uint64(count)) % len(files)
	removed := 0
	for i := 0; i < count; i++ {
		entry := files[(start+i)%len(files)]
		path := filepath.Join(dir, entry.Name())
		if strings.HasPrefix(entry.Name(), ".submission-environment-") {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return removed, infoErr
			}
			if time.Since(info.ModTime()) >= abandonedSubmissionEnvironmentAge {
				if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					return removed, removeErr
				}
				removed++
			}
			continue
		}
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
		terminal := getErr == nil && trig.Status == "done"
		if errors.Is(getErr, store.ErrNotFound) {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return removed, infoErr
			}
			terminal = time.Since(info.ModTime()) >= abandonedSubmissionEnvironmentAge
		}
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
