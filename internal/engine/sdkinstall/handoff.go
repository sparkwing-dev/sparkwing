package sdkinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

type installationHandoff struct {
	Engine           engineid.Identity
	Version          string
	Pin              engineid.Pin
	Directory        string
	Environment      string
	HashAllFiles     bool
	Key              string
	CachedExecutable string
	Digest           string
	Inputs           *bincache.InputWitness
}

func handoffEnvironment(environment procenv.Env) string {
	values := slices.Collect(environment.Values())
	// safety: temporary file placement does not select compiler inputs and differs across processes.
	values = slices.DeleteFunc(values, func(value string) bool { return strings.HasPrefix(value, "TMPDIR=") })
	slices.Sort(values)
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(sum[:])
}

func executableDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeHandoff(ctx context.Context, version, dir string, pin engineid.Pin, request engine.SDKInstallRequest, environment procenv.Env, command engine.SDKCommand, inputs *bincache.InputWitness) string {
	if command.CachedExecutable == "" || len(command.Command) != 1 || !inputs.Unchanged(ctx) {
		return ""
	}
	digest, err := executableDigest(command.Command[0])
	if err != nil {
		return ""
	}
	receipt := installationHandoff{Engine: request.Engine, Version: version, Pin: pin, Directory: dir, Environment: handoffEnvironment(environment), HashAllFiles: request.HashAllFiles, Key: filepath.Base(filepath.Dir(command.CachedExecutable)), CachedExecutable: command.CachedExecutable, Digest: digest, Inputs: inputs}
	body, err := json.Marshal(receipt)
	if err != nil {
		return ""
	}
	path := filepath.Join(request.ExecutableDirectory, "handoff.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return ""
	}
	return path
}

func restoreHandoff(ctx context.Context, version, dir string, pin engineid.Pin, request engine.SDKInstallRequest, environment procenv.Env) (engine.SDKCommand, bool) {
	if request.Handoff == "" || request.NoBincache || request.SDK != engineid.GoSDK || !filepath.IsAbs(request.Handoff) {
		return engine.SDKCommand{}, false
	}
	f, err := fssecure.OpenPrivateConfig(request.Handoff)
	if err != nil {
		return engine.SDKCommand{}, false
	}
	var receipt installationHandoff
	err = json.NewDecoder(io.LimitReader(f, 16<<20)).Decode(&receipt)
	closeErr := f.Close()
	if err != nil || closeErr != nil || receipt.Engine != request.Engine || receipt.Version != version || receipt.Pin != pin || receipt.Directory != dir || receipt.Environment != handoffEnvironment(environment) || receipt.HashAllFiles != request.HashAllFiles || receipt.Inputs == nil {
		return engine.SDKCommand{}, false
	}
	entry, err := bincache.PipelineEntry(receipt.Key)
	if err != nil {
		return engine.SDKCommand{}, false
	}
	lease, found, err := entry.Acquire(ctx)
	if err != nil || !found {
		return engine.SDKCommand{}, false
	}
	defer func() {
		if err := lease.Release(); err != nil {
			slog.WarnContext(ctx, "release local SDK handoff lease", "error", err)
		}
	}()
	if lease.CachedPath() != receipt.CachedExecutable {
		return engine.SDKCommand{}, false
	}
	path := filepath.Join(request.ExecutableDirectory, "pipeline")
	binary, err := lease.Install(path)
	if err != nil {
		return engine.SDKCommand{}, false
	}
	digest, err := executableDigest(binary.Path())
	if err != nil || digest != receipt.Digest || !receipt.Inputs.Valid(ctx) {
		if err := os.Remove(path); err != nil {
			slog.WarnContext(ctx, "remove rejected local SDK handoff", "error", err)
		}
		return engine.SDKCommand{}, false
	}
	return engine.SDKCommand{Directory: dir, Command: []string{binary.Path()}, CachedExecutable: lease.CachedPath(), Reused: true}, true
}
