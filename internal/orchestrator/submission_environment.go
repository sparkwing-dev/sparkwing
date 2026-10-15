package orchestrator

import (
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

	"github.com/sparkwing-dev/sparkwing/internal/envredact"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

const SubmissionEnvironmentCapturedKey = "_SPARKWING_SUBMISSION_ENV_CAPTURED"

const submissionEnvironmentDir = "submission-environments"

type submissionEnvironmentSnapshot struct {
	RunID       string   `json:"run_id"`
	Environment []string `json:"environment"`
}

func CaptureSubmissionEnvironment(home, runID string, env []string, logger *slog.Logger) (err error) {
	home, err = wingd.HomeDir(home)
	if err != nil {
		return err
	}
	captured, err := filterSubmissionEnvironment(env, logger)
	if err != nil {
		return err
	}
	dir := filepath.Join(home, submissionEnvironmentDir)
	if err := fssecure.EnsureDir(dir); err != nil {
		return fmt.Errorf("secure submission environment directory: %w", err)
	}
	body, err := json.Marshal(submissionEnvironmentSnapshot{RunID: runID, Environment: captured})
	if err != nil {
		return fmt.Errorf("encode submission environment: %w", err)
	}
	path := submissionEnvironmentPath(home, runID)
	privateDir, err := fssecure.MkdirPrivateTemp(dir, ".submission-environment-")
	if err != nil {
		return fmt.Errorf("create private submission environment directory: %w", err)
	}
	defer func() { err = errors.Join(err, os.Remove(privateDir)) }()
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
	home, err := wingd.HomeDir(home)
	if err != nil {
		return err
	}
	err = os.Remove(submissionEnvironmentPath(home, runID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func filterSubmissionEnvironment(env []string, logger *slog.Logger) ([]string, error) {
	names, prefixes, err := submissionEnvironmentAllowList()
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
			"names", strings.Join(dropped, ","), "allow_key", submitEnvAllowKey)
	}
	return out, nil
}

func submissionEnvironmentAllowList() (map[string]bool, []string, error) {
	items, path, err := submitEnvAllowSetting()
	if err != nil {
		return nil, nil, err
	}
	names := map[string]bool{}
	var prefixes []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		switch {
		case item == "":
		case item == "*":
			return nil, nil, fmt.Errorf(
				"%s %s: %q is not a wildcard; name each variable or give a prefix such as AWS_*",
				path, submitEnvAllowKey, item)
		case strings.HasSuffix(item, "*"):
			prefixes = append(prefixes, strings.TrimSuffix(item, "*"))
		default:
			names[item] = true
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

func submissionEnvironmentPath(home, runID string) string {
	sum := sha256.Sum256([]byte(runID))
	return filepath.Join(home, submissionEnvironmentDir, hex.EncodeToString(sum[:])+".json")
}
