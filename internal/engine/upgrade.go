package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// UpgradeRequest chooses an exact release or the signed index's latest permitted release.
type UpgradeRequest struct {
	Checkout string
	SDK      SDKKind
	Version  string
	DryRun   bool
}

// PinEdit preserves the source digest so an upgrade cannot overwrite concurrent user edits.
type PinEdit struct {
	Path         string
	BeforeDigest string
	After        []byte
}

// UpgradePlan contains concrete pin edits and the matching verified engine selection.
type UpgradePlan struct {
	Before       Pin
	After        Pin
	Edits        []PinEdit
	Engine       Identity
	Notices      []Notice
	inputDigests map[string]string
}

// UpgradeVerifier checks dependency resolution, Go compilation or TypeScript type checking,
type UpgradeVerifier interface {
	Verify(context.Context, string, Resolved) error
}

// UpgradeResult reports verification separately from a dry-run preview.
type UpgradeResult struct {
	Plan     UpgradePlan
	Applied  bool
	Verified bool
	DryRun   bool
}

// SoftwareUpdate keeps a verified runner replacement separate from an SDK-matched pipeline engine.
type SoftwareUpdate struct {
	CurrentVersion string
	Version        string
	Release        VerifiedRelease
	Required       bool
	Notices        []Notice
}

// Upgrade verifies staged code before applying digest-fenced manifest and lockfile edits.
func (r *Resolver) Upgrade(ctx context.Context, request UpgradeRequest, verifier UpgradeVerifier) (result UpgradeResult, err error) {
	if !filepath.IsAbs(request.Checkout) {
		return result, errors.New("upgrade requires an absolute checkout root")
	}
	request.Checkout, err = filepath.EvalSymlinks(request.Checkout)
	if err != nil {
		return result, err
	}
	pipeline, err := (SDKInstallRequest{Directory: request.Checkout, PipelineDir: ".sparkwing"}).PipelineDirectory()
	if err != nil {
		return result, err
	}
	stage, plan, resolved, err := r.prepareUpgrade(ctx, request, pipeline)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(stage)) }()
	stagePipeline := filepath.Join(stage, ".sparkwing")
	result = UpgradeResult{Plan: plan, DryRun: request.DryRun}
	if request.DryRun {
		return result, nil
	}
	if verifier == nil {
		return result, invalid("upgrade verification is required", "Supply the dependency, build and describe/plan verifier.")
	}
	if err := verifier.Verify(ctx, stage, resolved); err != nil {
		return result, fmt.Errorf("SDK upgrade verification failed; repository pins are unchanged: %w", err)
	}
	after, err := ReadPin(stagePipeline, request.SDK)
	if err != nil {
		return result, err
	}
	if after.Version != plan.After.Version {
		return result, integrity(fmt.Errorf("upgrade verifier changed the selected SDK version"))
	}
	plan.After = after
	plan.Engine.Pin = after
	plan.Edits, err = collectPinEdits(stagePipeline, plan.Before, after, plan.inputDigests)
	if err != nil {
		return result, err
	}
	result.Plan = plan
	result.Verified = true
	currentInputs, err := readPinInputDigests(pipeline, request.SDK)
	if err != nil {
		return result, err
	}
	for path, expected := range plan.inputDigests {
		if currentInputs[path] != expected {
			return result, &apierr.Error{Code: apierr.Conflict, Message: "Repository pin changed during upgrade verification: " + path, Hint: "Review the concurrent edit, then retry sparkwing upgrade."}
		}
	}
	if err := applyPinEdits(ctx, request.Checkout, plan.Edits); err != nil {
		return result, err
	}
	result.Applied = true
	return result, nil
}

func (r *Resolver) prepareUpgrade(ctx context.Context, request UpgradeRequest, pipeline string) (stage string, plan UpgradePlan, resolved Resolved, result error) {
	if err := ctx.Err(); err != nil {
		return "", plan, resolved, err
	}
	before, err := ReadPin(pipeline, request.SDK)
	if err != nil {
		return "", plan, resolved, err
	}
	policy, err := r.Index(ctx, time.Now())
	if err != nil {
		return "", plan, resolved, err
	}
	version := request.Version
	if version == "" {
		version = policy.Index.index.Latest
		if policy.Index.Development() {
			return "", plan, resolved, invalid("development builds require an explicit upgrade version", "Use sparkwing upgrade --version vMAJOR.MINOR.PATCH.")
		}
	}
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	if _, err := Evaluate(policy.Index, version, time.Now()); err != nil {
		return "", plan, resolved, err
	}
	stage, err = os.MkdirTemp(r.config.CacheDirectory, "upgrade-")
	if err != nil {
		return "", plan, resolved, err
	}
	stagePipeline := filepath.Join(stage, ".sparkwing")
	defer func() {
		if result != nil {
			result = errors.Join(result, os.RemoveAll(stage))
		}
	}()
	if err := copyCheckout(ctx, request.Checkout, stage); err != nil {
		return stage, plan, resolved, err
	}
	snapshot, err := ReadPin(stagePipeline, request.SDK)
	if err != nil {
		return stage, plan, resolved, err
	}
	if snapshot != before {
		return stage, plan, resolved, &apierr.Error{Code: apierr.Conflict, Message: "Repository pins changed while staging the upgrade", Hint: "Retry the upgrade after the concurrent edit finishes."}
	}
	inputDigests, err := readPinInputDigests(stagePipeline, request.SDK)
	if err != nil {
		return stage, plan, resolved, err
	}
	if err := r.updateNativePin(ctx, stagePipeline, request.SDK, version); err != nil {
		return stage, plan, resolved, err
	}
	after, err := ReadPin(stagePipeline, request.SDK)
	if err != nil {
		return stage, plan, resolved, err
	}
	actual, err := VersionForPin(after)
	if err != nil {
		return stage, plan, resolved, err
	}
	if actual != version {
		return stage, plan, resolved, integrity(fmt.Errorf("native dependency resolver did not retain requested SDK pin"))
	}
	resolved, err = r.Resolve(ctx, after, releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH})
	if err != nil {
		return stage, plan, resolved, err
	}
	edits, err := collectPinEdits(stagePipeline, before, after, inputDigests)
	if err != nil {
		return stage, plan, resolved, err
	}
	plan = UpgradePlan{Before: before, After: after, Edits: edits, Engine: resolved.Identity, Notices: resolved.Notices, inputDigests: inputDigests}
	return stage, plan, resolved, nil
}

func (r *Resolver) updateNativePin(ctx context.Context, dir string, sdk SDKKind, version string) error {
	switch sdk {
	case GoSDK:
		body, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err != nil {
			return err
		}
		file, err := modfile.Parse("go.mod", body, nil)
		if err != nil {
			return err
		}
		if err := file.AddRequire(GoModule, version); err != nil {
			return err
		}
		body, err = file.Format()
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), body, 0o600); err != nil {
			return err
		}
		return r.nativeCommand(ctx, dir, Pin{SDK: GoSDK, Version: version}, "go", "mod", "tidy")
	case TypeScriptSDK:
		body, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil {
			return err
		}
		var manifest map[string]json.RawMessage
		if err := json.Unmarshal(body, &manifest); err != nil {
			return err
		}
		for _, section := range []string{"dependencies", "devDependencies", "peerDependencies"} {
			raw, ok := manifest[section]
			if !ok {
				continue
			}
			var deps map[string]json.RawMessage
			if err := json.Unmarshal(raw, &deps); err != nil {
				return err
			}
			if _, ok := deps[TypeScriptPackage]; ok {
				value, err := json.Marshal(strings.TrimPrefix(version, "v"))
				if err != nil {
					return err
				}
				deps[TypeScriptPackage] = value
				updated, err := json.Marshal(deps)
				if err != nil {
					return err
				}
				manifest[section] = updated
			}
		}
		body, err = json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return err
		}
		body = append(body, '\n')
		if err := os.WriteFile(filepath.Join(dir, "package.json"), body, 0o600); err != nil {
			return err
		}
		return r.nativeCommand(ctx, dir, Pin{SDK: TypeScriptSDK, Version: version}, "npm", "install", "--package-lock-only", "--ignore-scripts", "--no-audit", "--no-fund")
	default:
		return invalid("unknown SDK kind", "Select go or typescript.")
	}
}

func (r *Resolver) nativeCommand(ctx context.Context, dir string, pin Pin, name string, args ...string) error {
	if r.command != nil {
		return r.command(ctx, dir, name, args...)
	}
	command := exec.Command(name, args...)
	if name == "npm" {
		var err error
		command, err = pipelineentry.NPMCommand(ctx, runtime.GOOS, exec.LookPath, args...)
		if err != nil {
			return fmt.Errorf("resolve npm: %w", err)
		}
	}
	command.Dir = dir
	masker := secrets.NewMasker()
	environment, err := procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: os.Environ(), Masker: masker}, procenv.Runtime{})
	if err != nil {
		return err
	}
	environment = append(environment, "GOWORK=off")
	if name == "go" {
		qa, err := QAModuleEnvironment(ctx, dir, pin, environment)
		if err != nil {
			return err
		}
		environment = append(environment, qa...)
	}
	command.Env = environment
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := gotoolchain.Run(ctx, command); err != nil {
		return fmt.Errorf("resolve native dependency lockfiles: %w: %s", err, strings.TrimSpace(masker.Mask(output.String())))
	}
	return nil
}

func copyCheckout(ctx context.Context, source, destination string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		// safety: a host may keep its engine cache inside the checkout being staged.
		if path == destination {
			return filepath.SkipDir
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "node_modules") {
			return filepath.SkipDir
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.Mkdir(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(source, resolved)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("upgrade staging cannot copy a symlink outside the checkout: %s", relative)
			}
			if filepath.IsAbs(link) {
				link = filepath.Join(destination, rel)
			}
			return os.Symlink(link, target)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("upgrade staging cannot copy non-regular file: %s", relative)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, info.Mode().Perm())
	})
}

func collectPinEdits(stage string, before, after Pin, inputs map[string]string) ([]PinEdit, error) {
	paths := []string{before.Manifest}
	if before.Lockfile != "" {
		paths = append(paths, before.Lockfile)
	}
	if after.Lockfile != "" && after.Lockfile != before.Lockfile {
		paths = append(paths, after.Lockfile)
	}
	var edits []PinEdit
	for _, path := range paths {
		updated, err := os.ReadFile(filepath.Join(stage, path))
		if err != nil {
			return nil, err
		}
		beforeDigest := inputs[path]
		if beforeDigest == digest(updated) {
			continue
		}
		edits = append(edits, PinEdit{Path: filepath.Join(".sparkwing", path), BeforeDigest: beforeDigest, After: updated})
	}
	return edits, nil
}

func applyPinEdits(ctx context.Context, checkout string, edits []PinEdit) error {
	type original struct {
		body    []byte
		mode    os.FileMode
		missing bool
	}
	originals := make([]original, len(edits))
	for i, edit := range edits {
		if !filepath.IsLocal(edit.Path) {
			return integrity(fmt.Errorf("upgrade edit escapes checkout"))
		}
		path := filepath.Join(checkout, edit.Path)
		body, err := os.ReadFile(path)
		missing := os.IsNotExist(err)
		if err != nil && !missing {
			return err
		}
		hash := ""
		mode := os.FileMode(0o600)
		if !missing {
			hash = digest(body)
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("pin edit is not a regular file")
			}
			mode = info.Mode().Perm()
		}
		if hash != edit.BeforeDigest {
			return &apierr.Error{Code: apierr.Conflict, Message: "Repository pin changed during upgrade verification: " + edit.Path, Hint: "Review the concurrent edit, then retry sparkwing upgrade."}
		}
		originals[i] = original{body: body, mode: mode, missing: missing}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for i, edit := range edits {
		if err := atomicWrite(filepath.Join(checkout, edit.Path), edit.After, originals[i].mode); err != nil {
			result := err
			for j := i - 1; j >= 0; j-- {
				path := filepath.Join(checkout, edits[j].Path)
				if originals[j].missing {
					result = errors.Join(result, os.Remove(path))
				} else {
					result = errors.Join(result, atomicWrite(path, originals[j].body, originals[j].mode))
				}
			}
			return result
		}
	}
	return nil
}

// PrepareRunnerUpdate obtains a verified replacement even when the running version is blocked.
func (r *Resolver) PrepareRunnerUpdate(ctx context.Context, currentVersion string, target releaseasset.Target) (SoftwareUpdate, error) {
	if target.Binary != releaseasset.SparkwingRunner {
		return SoftwareUpdate{}, invalid("runner updates require sparkwing-runner", "Select the runner executable target.")
	}
	now := time.Now()
	policy, err := r.Index(ctx, now)
	if err != nil {
		return SoftwareUpdate{}, err
	}
	if policy.Index.Development() {
		return SoftwareUpdate{CurrentVersion: currentVersion, Version: currentVersion}, nil
	}
	if _, err := target.NameForVersion(currentVersion); err != nil {
		return SoftwareUpdate{}, err
	}
	notices, policyErr := EvaluateRunner(policy.Index, currentVersion, now)
	required := false
	if policyErr != nil {
		var api *apierr.Error
		if !errors.As(policyErr, &api) || (api.Code != apierr.VersionRevoked && api.Code != apierr.VersionUnsupported) {
			return SoftwareUpdate{}, policyErr
		}
		required = true
	}
	version := policy.Index.index.Latest
	replacementNotices, err := EvaluateRunner(policy.Index, version, now)
	if err != nil {
		return SoftwareUpdate{}, err
	}
	update := SoftwareUpdate{CurrentVersion: currentVersion, Version: version, Required: required, Notices: append(append(policy.Notices, notices...), replacementNotices...)}
	if semver.Compare(currentVersion, version) >= 0 && !required {
		update.Version = currentVersion
		return update, nil
	}
	identity := Identity{Pin: Pin{SDK: GoSDK, Version: version}, Version: version}
	cached, ok, err := r.findCached(ctx, identity, target)
	if err != nil {
		return update, err
	}
	if ok {
		update.Release, err = readCachedRelease(version, target, cached.Executable)
		return update, err
	}
	update.Release, err = r.fetchRelease(ctx, version, target)
	if err != nil {
		return update, &apierr.Error{Code: apierr.Unavailable, Message: fmt.Sprintf("Runner update to %s failed: %v", version, err), Hint: runnerHint}
	}
	if _, err := r.StoreVerified(ctx, identity.Pin, update.Release); err != nil {
		return update, err
	}
	return update, nil
}

func readPinInputDigests(checkout string, sdk SDKKind) (map[string]string, error) {
	paths := []string{"go.mod", "go.sum"}
	if sdk == TypeScriptSDK {
		paths = []string{"package.json", "package-lock.json", "npm-shrinkwrap.json"}
	}
	inputs := map[string]string{}
	for _, path := range paths {
		body, err := os.ReadFile(filepath.Join(checkout, path))
		if os.IsNotExist(err) {
			inputs[path] = ""
			continue
		}
		if err != nil {
			return nil, err
		}
		inputs[path] = digest(body)
	}
	return inputs, nil
}
