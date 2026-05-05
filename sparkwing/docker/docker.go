// Package docker is the sparkwing SDK's Docker-shelling helper layer:
// build, push, login, and deterministic tag computation.
//
// Free functions, (T, error) returns, no must-variants. Shell-outs
// are context-aware so cancellation and deadlines propagate. Secrets
// pass via stdin, never argv.
//
// Leaf package: the only allowed sparkwing import is the sibling
// sparkwing/git subpackage. No registry provider is baked in;
// opinionated flows (ECR login, kind-load shortcuts) live in
// downstream libraries.
package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

// ErrDockerUnavailable is returned by every function that shells out
// to docker when the docker binary is not on PATH. Callers detect this
// with errors.Is to skip Docker-dependent pipeline steps cleanly.
var ErrDockerUnavailable = errors.New("docker: binary not available on PATH")

// ErrBuildxRequired is returned by Build / BuildAndPush when the caller
// asks for multi-platform builds (non-empty Platforms) but `docker
// buildx` is not available.
var ErrBuildxRequired = errors.New("docker: buildx plugin required for multi-platform builds")

// ErrPlatformUnsupported is returned by Build / BuildAndPush when the
// caller requests a platform the active buildx builder does not
// advertise. The wrapped message names the unsupported entries and
// the builder's advertised list.
var ErrPlatformUnsupported = errors.New("docker: requested platform not supported by active buildx builder")

// BuildConfig configures a Docker image build and optional push.
type BuildConfig struct {
	// Image is the image name without registry prefix (e.g. "myapp").
	Image string
	// Dockerfile is the path to the Dockerfile. Defaults to "Dockerfile".
	Dockerfile string
	// Context is the build context directory. Defaults to ".".
	Context string
	// Platforms is the list of target platforms (e.g.
	// ["linux/amd64", "linux/arm64"]). Empty means local-arch
	// single-platform build via plain `docker build`. Non-empty
	// switches to `docker buildx build` and requires buildx.
	Platforms []string
	// Registries is the list of registries to push to. Empty means
	// local build only, no push.
	Registries []string
	// Tags is the list of tags to apply (and push when registries are
	// set). At least one tag is required.
	Tags []string
	// BuildArgs are passed via --build-arg KEY=VAL.
	BuildArgs map[string]string
	// Labels are passed via --label KEY=VAL.
	Labels map[string]string
}

// BuildResult is returned by Build / BuildAndPush.
type BuildResult struct {
	// Image is the fully-qualified image name pushed to the first
	// registry (if any), otherwise the local image reference.
	Image string
	// Digests maps each pushed "<registry>/<image>:<tag>" reference to
	// its pushed digest, when one could be resolved. Populated by
	// BuildAndPush and Push only.
	Digests map[string]string
	// Registries is the list of registries successfully pushed to.
	Registries []string
}

// Build runs `docker build` (or `docker buildx build` when Platforms
// is non-empty) and applies the tags locally. It does not push.
func Build(ctx context.Context, cfg BuildConfig) (BuildResult, error) {
	planguard.Guard(ctx, "docker.Build")
	return build(ctx, cfg, false)
}

// BuildAndPush builds the image (same rules as Build) and pushes it
// to every configured registry. For multi-platform builds, buildx
// handles push in-line via --push.
func BuildAndPush(ctx context.Context, cfg BuildConfig) (BuildResult, error) {
	planguard.Guard(ctx, "docker.BuildAndPush")
	return build(ctx, cfg, true)
}

func build(ctx context.Context, cfg BuildConfig, push bool) (BuildResult, error) {
	if err := ensureDocker(); err != nil {
		return BuildResult{}, err
	}
	if cfg.Image == "" {
		return BuildResult{}, errors.New("docker: BuildConfig.Image is required")
	}
	if len(cfg.Tags) == 0 {
		return BuildResult{}, errors.New("docker: BuildConfig.Tags must have at least one entry")
	}
	if cfg.Dockerfile == "" {
		cfg.Dockerfile = "Dockerfile"
	}
	if cfg.Context == "" {
		cfg.Context = "."
	}

	multiPlatform := len(cfg.Platforms) > 0
	if multiPlatform {
		if !buildxAvailable(ctx) {
			return BuildResult{}, ErrBuildxRequired
		}
		// Pre-flight: fail fast if any requested platform is missing
		// from the builder's advertised list. Best-effort: on
		// introspection failure, let buildx surface the actual error.
		if advertised, err := BuildxPlatforms(ctx); err == nil && len(advertised) > 0 {
			var missing []string
			for _, p := range cfg.Platforms {
				if !platformAdvertised(p, advertised) {
					missing = append(missing, p)
				}
			}
			if len(missing) > 0 {
				return BuildResult{}, fmt.Errorf("%w: %v not in builder's advertised platforms %v; install QEMU emulation (e.g. `docker run --privileged --rm tonistiigi/binfmt --install all`) or trim BuildConfig.Platforms",
					ErrPlatformUnsupported, missing, advertised)
			}
		}
	}

	localRefs := make([]string, 0, len(cfg.Tags))
	for _, t := range cfg.Tags {
		localRefs = append(localRefs, fmt.Sprintf("%s:%s", cfg.Image, t))
	}

	remoteRefs := make([]string, 0, len(cfg.Registries)*len(cfg.Tags))
	for _, reg := range cfg.Registries {
		for _, t := range cfg.Tags {
			remoteRefs = append(remoteRefs, fmt.Sprintf("%s/%s:%s", reg, cfg.Image, t))
		}
	}

	var args []string
	if multiPlatform {
		args = append(args, "buildx", "build", "--platform", strings.Join(cfg.Platforms, ","))
		if push && len(cfg.Registries) > 0 {
			args = append(args, "--push")
		} else if len(cfg.Platforms) == 1 {
			// Single-platform buildx build can load into the local
			// daemon; multi-platform builds cannot be loaded.
			args = append(args, "--load")
		}
	} else {
		args = append(args, "build")
	}

	args = append(args, "-f", cfg.Dockerfile)

	// In multi-platform + push mode buildx pushes every -t target;
	// bare `image:tag` tags resolve to docker.io, which we have no
	// credentials for, so omit them.
	includeLocal := !(multiPlatform && push && len(cfg.Registries) > 0)
	if includeLocal {
		for _, t := range localRefs {
			args = append(args, "-t", t)
		}
	}
	if multiPlatform {
		// Include remote refs in the build itself so buildx pushes
		// them directly.
		for _, r := range remoteRefs {
			args = append(args, "-t", r)
		}
	}

	for k, v := range cfg.BuildArgs {
		args = append(args, "--build-arg", fmt.Sprintf("%s=%s", k, v))
	}
	for k, v := range cfg.Labels {
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, cfg.Context)

	if _, err := runDocker(ctx, nil, args...); err != nil {
		return BuildResult{}, fmt.Errorf("docker build: %w", err)
	}

	result := BuildResult{
		Image:   firstOrEmpty(localRefs),
		Digests: map[string]string{},
	}

	if !push || len(cfg.Registries) == 0 {
		return result, nil
	}

	if multiPlatform {
		// buildx already pushed everything inline.
		result.Registries = append(result.Registries, cfg.Registries...)
		if len(remoteRefs) > 0 {
			result.Image = remoteRefs[0]
		}
		return result, nil
	}

	// Single-platform path: we must tag and push each remote ref.
	primaryLocal := localRefs[0]

	var pushed []string
	var errs []error
	pushedRegistries := map[string]struct{}{}

	for _, reg := range cfg.Registries {
		regSucceeded := true
		for _, t := range cfg.Tags {
			remote := fmt.Sprintf("%s/%s:%s", reg, cfg.Image, t)
			if _, err := runDocker(ctx, nil, "tag", primaryLocal, remote); err != nil {
				errs = append(errs, fmt.Errorf("docker tag %s -> %s: %w", primaryLocal, remote, err))
				regSucceeded = false
				continue
			}
			if _, err := runDocker(ctx, nil, "push", remote); err != nil {
				errs = append(errs, fmt.Errorf("docker push %s: %w", remote, err))
				regSucceeded = false
				continue
			}
			pushed = append(pushed, remote)
		}
		if regSucceeded {
			pushedRegistries[reg] = struct{}{}
		}
	}

	for _, reg := range cfg.Registries {
		if _, ok := pushedRegistries[reg]; ok {
			result.Registries = append(result.Registries, reg)
		}
	}
	if len(pushed) > 0 {
		result.Image = pushed[0]
	}
	if err := errors.Join(errs...); err != nil {
		return result, err
	}
	return result, nil
}

// Push tags and pushes the given local `image` reference to each
// registry with every requested tag. Best-effort: a single registry
// failure does not abort the rest; errors are joined.
func Push(ctx context.Context, image string, tags, registries []string) error {
	planguard.Guard(ctx, "docker.Push")
	if err := ensureDocker(); err != nil {
		return err
	}
	if image == "" {
		return errors.New("docker: Push image is required")
	}
	if len(tags) == 0 {
		return errors.New("docker: Push requires at least one tag")
	}
	if len(registries) == 0 {
		return errors.New("docker: Push requires at least one registry")
	}

	// Strip any trailing :tag so the base reference is clean.
	base := image
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		base = image[:i]
	}

	var errs []error
	for _, reg := range registries {
		for _, t := range tags {
			remote := fmt.Sprintf("%s/%s:%s", reg, base, t)
			if _, err := runDocker(ctx, nil, "tag", image, remote); err != nil {
				errs = append(errs, fmt.Errorf("docker tag %s -> %s: %w", image, remote, err))
				continue
			}
			if _, err := runDocker(ctx, nil, "push", remote); err != nil {
				errs = append(errs, fmt.Errorf("docker push %s: %w", remote, err))
				continue
			}
		}
	}
	return errors.Join(errs...)
}

// Login authenticates docker with the given registry. The secret is
// piped via stdin to `docker login --password-stdin`, never placed on
// the command line.
func Login(ctx context.Context, registry, username, secret string) error {
	planguard.Guard(ctx, "docker.Login")
	if err := ensureDocker(); err != nil {
		return err
	}
	if registry == "" {
		return errors.New("docker: Login registry is required")
	}
	if username == "" {
		return errors.New("docker: Login username is required")
	}
	if secret == "" {
		return errors.New("docker: Login secret is required")
	}

	args := []string{"login", "--username", username, "--password-stdin", registry}
	if _, err := runDocker(ctx, strings.NewReader(secret), args...); err != nil {
		return fmt.Errorf("docker login %s: %w", registry, err)
	}
	return nil
}

func ensureDocker() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return ErrDockerUnavailable
	}
	return nil
}

func buildxAvailable(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "docker", "buildx", "version")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

// BuildxPlatforms returns the list of platforms the active buildx
// builder advertises (e.g. ["linux/arm64", "linux/amd64", ...]).
// Aggregates across all nodes when the builder has multiple. The
// native-platform `*` marker is stripped.
//
// Returns ErrDockerUnavailable when docker is not on PATH and
// ErrBuildxRequired when the buildx plugin is missing.
func BuildxPlatforms(ctx context.Context) ([]string, error) {
	planguard.Guard(ctx, "docker.BuildxPlatforms")
	if err := ensureDocker(); err != nil {
		return nil, err
	}
	if !buildxAvailable(ctx) {
		return nil, ErrBuildxRequired
	}
	out, err := runDocker(ctx, nil, "buildx", "inspect")
	if err != nil {
		return nil, fmt.Errorf("docker buildx inspect: %w", err)
	}
	return parseBuildxPlatforms(out), nil
}

// FilterBuildxPlatforms returns the subset of `wish` that the active
// buildx builder can build. Matching is loose: a wish of
// "linux/amd64" is satisfied by "linux/amd64", "linux/amd64*", or
// any "linux/amd64/<variant>" entry. Order is preserved relative to
// `wish`. Returns the same errors as BuildxPlatforms.
func FilterBuildxPlatforms(ctx context.Context, wish []string) ([]string, error) {
	planguard.Guard(ctx, "docker.FilterBuildxPlatforms")
	if len(wish) == 0 {
		return nil, nil
	}
	advertised, err := BuildxPlatforms(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(wish))
	for _, w := range wish {
		if platformAdvertised(w, advertised) {
			out = append(out, w)
		}
	}
	return out, nil
}

// parseBuildxPlatforms extracts every `Platforms:` line from a
// `docker buildx inspect` text dump. Strips the native-marker `*`
// and dedupes.
func parseBuildxPlatforms(raw string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Platforms:") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "Platforms:"))
		for _, entry := range strings.Split(rest, ",") {
			entry = strings.TrimSpace(entry)
			entry = strings.TrimSuffix(entry, "*")
			if entry == "" {
				continue
			}
			if _, ok := seen[entry]; ok {
				continue
			}
			seen[entry] = struct{}{}
			out = append(out, entry)
		}
	}
	return out
}

// platformAdvertised reports whether `wish` is covered by any entry
// in `advertised`. Variant suffixes ("linux/amd64/v2") satisfy the
// base wish ("linux/amd64").
func platformAdvertised(wish string, advertised []string) bool {
	for _, a := range advertised {
		if a == wish || strings.HasPrefix(a, wish+"/") {
			return true
		}
	}
	return false
}

func runDocker(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return stdout.String(), nil
}

func firstOrEmpty(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}
