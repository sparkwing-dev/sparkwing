package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

type installationHandoff struct {
	Engine           engineid.Identity
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

func sdkExecutableDigest(path string) (string, error) {
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

// WriteSDKHandoff records a verified invocation for reuse by a local owner.
func WriteSDKHandoff(dir string, request SDKInstallRequest, environment procenv.Env, command SDKCommand, inputs *bincache.InputWitness) string {
	if command.CachedExecutable == "" || len(command.Command) != 1 || inputs == nil || inputs.Disabled || len(inputs.States) == 0 || inputs.Tool == "" || len(inputs.Settings) == 0 {
		return ""
	}
	digest, err := sdkExecutableDigest(command.Command[0])
	if err != nil {
		return ""
	}
	receipt := installationHandoff{Engine: request.Engine, Directory: dir, Environment: handoffEnvironment(environment), HashAllFiles: request.HashAllFiles, Key: filepath.Base(filepath.Dir(command.CachedExecutable)), CachedExecutable: command.CachedExecutable, Digest: digest, Inputs: inputs}
	body, err := json.Marshal(receipt)
	if err != nil {
		return ""
	}
	path := filepath.Join(request.ExecutableDirectory, "handoff.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return ""
	}
	return sdkHandoffSeal(body, path)
}

func sdkHandoffSeal(body []byte, path string) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]) + ":" + path
}

func sdkHandoffPath(seal string) (string, bool) {
	digest, path, ok := strings.Cut(seal, ":")
	return path, ok && validDigest(digest) && filepath.IsAbs(path)
}

func restoreSDKHandoff(ctx context.Context, dir string, request SDKInstallRequest, environment procenv.Env) (SDKCommand, bool) {
	path, valid := sdkHandoffPath(request.Handoff)
	if !valid || request.NoBincache || request.SDK != engineid.GoSDK {
		return SDKCommand{}, false
	}
	f, err := fssecure.OpenPrivateConfig(path)
	if err != nil {
		return SDKCommand{}, false
	}
	body, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	closeErr := f.Close()
	if err != nil || closeErr != nil || len(body) > 16<<20 || sdkHandoffSeal(body, path) != request.Handoff {
		return SDKCommand{}, false
	}
	var receipt installationHandoff
	err = json.Unmarshal(body, &receipt)
	if err != nil || closeErr != nil || receipt.Engine != request.Engine || receipt.Directory != dir || receipt.Environment != handoffEnvironment(environment) || receipt.HashAllFiles != request.HashAllFiles || receipt.Inputs == nil {
		return SDKCommand{}, false
	}
	entry, err := bincache.PipelineEntry(receipt.Key)
	if err != nil {
		return SDKCommand{}, false
	}
	lease, found, err := entry.Acquire(ctx)
	if err != nil || !found {
		return SDKCommand{}, false
	}
	defer func() {
		if err := lease.Release(); err != nil {
			slog.WarnContext(ctx, "release local SDK handoff lease", "error", err)
		}
	}()
	if lease.CachedPath() != receipt.CachedExecutable {
		return SDKCommand{}, false
	}
	name := "pipeline"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path = filepath.Join(request.ExecutableDirectory, name)
	binary, err := lease.Install(path)
	if err != nil {
		return SDKCommand{}, false
	}
	digest, err := sdkExecutableDigest(binary.Path())
	if err != nil || digest != receipt.Digest || !receipt.Inputs.Valid(ctx) {
		if err := os.Remove(path); err != nil {
			slog.WarnContext(ctx, "remove rejected local SDK handoff", "error", err)
		}
		return SDKCommand{}, false
	}
	return SDKCommand{Directory: dir, Command: []string{binary.Path()}, CachedExecutable: lease.CachedPath(), Reused: true}, true
}
