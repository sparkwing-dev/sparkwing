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
	"runtime"
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

const (
	submissionEnvironmentDir          = "submission-environments"
	abandonedSubmissionEnvironmentAge = 10 * time.Minute
)

var submissionEnvironmentReconcileCursors sync.Map

type SubmissionEnvironmentUnavailableError struct {
	RunID string
}

func (e *SubmissionEnvironmentUnavailableError) Error() string {
	return fmt.Sprintf("run %s: submission execution environment is unavailable; submit a new run from the intended environment", e.RunID)
}

type RetryEnvironmentUnavailableError struct {
	RunID string
}

func (e *RetryEnvironmentUnavailableError) Error() string {
	return fmt.Sprintf("run %s: retry execution environment is unavailable; submit a new run from the intended environment", e.RunID)
}

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
	privateDir, err := fssecure.MkdirPrivateTemp(dir, ".submission-environment-")
	if err != nil {
		return fmt.Errorf("create private submission environment directory: %w", err)
	}
	defer func() { _ = os.Remove(privateDir) }()
	tmp, err := os.CreateTemp(privateDir, "snapshot-*")
	if err != nil {
		return fmt.Errorf("create submission environment: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := fssecure.SecurePrivateConfig(tmpPath); err != nil {
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
	if err := syncSubmissionEnvironmentDirectory(dir); err != nil {
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
		if trig.RetryOf != "" {
			return nil, &RetryEnvironmentUnavailableError{RunID: trig.ID}
		}
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
		// safety: snapshots outlive the shell, so credential values and names are filtered before publication.
		if submissionEnvironmentCredentialName(key, value) || envredact.CredentialValue(value) || envredact.RedactValue(value) != value {
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
	if nativeSubmissionEnvironmentAllowed(key) || envAllowed(key) || names[key] {
		return true
	}
	for _, prefix := range prefixes {
		if prefix != "" && strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func nativeSubmissionEnvironmentAllowed(key string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	switch strings.ToUpper(key) {
	case "PATH", "HOME", "USERPROFILE", "SYSTEMROOT", "COMSPEC", "TEMP", "TMP", "APPDATA", "LOCALAPPDATA", "SPARKWING_CONFIG", "SPARKWING_SECRETS_KEY_FILE":
		return true
	}
	return false
}

func submissionEnvironmentCredentialName(key, value string) bool {
	// safety: an absolute key-file path selects the private file without carrying its key bytes.
	if runtime.GOOS == "windows" && strings.EqualFold(key, "SPARKWING_SECRETS_KEY_FILE") && filepath.IsAbs(value) {
		return false
	}
	return envredact.CredentialName(key)
}

func consumeSubmissionEnvironment(home string, trig *store.Trigger, logger *slog.Logger) ([]string, error) {
	env, err := submissionEnvironment(home, trig)
	if err == nil && env == nil && trig.RetryOf != "" {
		err = &RetryEnvironmentUnavailableError{RunID: trig.ID}
	}
	if err == nil && env == nil && (strings.HasPrefix(trig.TriggerSource, "runs-submit") || trig.TriggerEnv[SubmitRepoDirKey] != "") {
		err = &SubmissionEnvironmentUnavailableError{RunID: trig.ID}
	}
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
		if (!entry.IsDir() && strings.HasSuffix(entry.Name(), ".json")) || strings.HasPrefix(entry.Name(), ".submission-environment-") {
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
				var removeErr error
				if entry.IsDir() {
					removeErr = removeAbandonedSubmissionDirectory(path)
				} else {
					removeErr = os.Remove(path)
				}
				if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
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
		terminal := getErr == nil && trig.IsFinished()
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

func removeAbandonedSubmissionDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("submission temporary path is not a directory: %s", path)
	}
	if err := fssecure.VerifyPrivateConfig(path, info); err != nil {
		return fmt.Errorf("submission temporary directory is not private: %w", err)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	// safety: inspect every entry before removing anything; unknown files and links are not capture residue.
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !strings.HasPrefix(entry.Name(), "snapshot-") || !info.Mode().IsRegular() {
			return fmt.Errorf("unexpected submission temporary entry: %s", filepath.Join(path, entry.Name()))
		}
		if err := fssecure.VerifyPrivateConfig(filepath.Join(path, entry.Name()), info); err != nil {
			return fmt.Errorf("submission temporary snapshot is not private: %w", err)
		}
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(path, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Remove(path)
}
