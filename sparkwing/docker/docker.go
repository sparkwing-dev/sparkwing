package docker

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
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
	Image string `json:"image"`
	// Dockerfile is the path to the Dockerfile. Defaults to "Dockerfile".
	Dockerfile string `json:"dockerfile"`
	// Context is the build context directory. Defaults to ".".
	Context string `json:"context"`
	// Platforms is the list of target platforms (e.g.
	// ["linux/amd64", "linux/arm64"]). Empty means local-arch
	// single-platform build via plain `docker build`. Non-empty
	// switches to `docker buildx build` and requires buildx.
	Platforms []string `json:"platforms"`
	// Registries is the list of registries to push to. Empty means
	// local build only, no push.
	Registries []string `json:"registries"`
	// Tags is the list of tags to apply (and push when registries are
	// set). At least one tag is required.
	Tags []string `json:"tags"`
	// BuildArgs are passed via --build-arg KEY=VAL.
	BuildArgs map[string]string `json:"buildArgs"`
	// Labels are passed via --label KEY=VAL.
	Labels map[string]string `json:"labels"`
}

// BuildResult is returned by Build / BuildAndPush.
type BuildResult struct {
	// Image is the fully-qualified image name pushed to the first
	// registry (if any), otherwise the local image reference.
	Image string `json:"image"`
	// Digests maps each pushed "<registry>/<image>:<tag>" reference to
	// its pushed digest, when one could be resolved. Populated by
	// BuildAndPush and Push only.
	Digests map[string]string `json:"digests"`
	// Registries is the list of registries successfully pushed to.
	Registries []string `json:"registries"`
}

// Build runs `docker build` (or `docker buildx build` when Platforms
// is non-empty) and applies the tags locally. It does not push.
func Build(ctx context.Context, cfg BuildConfig) (BuildResult, error) {
	var result BuildResult
	err := sdkserve.Helper(ctx, "docker.Build", []any{cfg}, &result)
	return result, err
}

// BuildAndPush builds the image (same rules as Build) and pushes it
// to every configured registry. For multi-platform builds, buildx
// handles push in-line via --push.
func BuildAndPush(ctx context.Context, cfg BuildConfig) (BuildResult, error) {
	var result BuildResult
	err := sdkserve.Helper(ctx, "docker.BuildAndPush", []any{cfg}, &result)
	return result, err
}

// Push tags and pushes the given local `image` reference to each
// registry with every requested tag. Best-effort: a single registry
// failure does not abort the rest; errors are joined.
func Push(ctx context.Context, image string, tags, registries []string) error {
	return sdkserve.Helper(ctx, "docker.Push", []any{image, tags, registries}, nil)
}

// Login authenticates docker with the given registry. The secret is
// piped via stdin to `docker login --password-stdin`, never placed on
// the command line.
func Login(ctx context.Context, registry, username, secret string) error {
	return sdkserve.Helper(ctx, "docker.Login", []any{registry, username, secret}, nil)
}

// BuildxPlatforms returns the list of platforms the active buildx
// builder advertises (e.g. ["linux/arm64", "linux/amd64", ...]).
// Aggregates across all nodes when the builder has multiple. The
// native-platform `*` marker is stripped.
//
// Returns ErrDockerUnavailable when docker is not on PATH and
// ErrBuildxRequired when the buildx plugin is missing.
func BuildxPlatforms(ctx context.Context) ([]string, error) {
	var result []string
	err := sdkserve.Helper(ctx, "docker.BuildxPlatforms", []any{}, &result)
	return result, err
}

// FilterBuildxPlatforms returns the subset of `wish` that the active
// buildx builder can build. Matching is loose: a wish of
// "linux/amd64" is satisfied by "linux/amd64", "linux/amd64*", or
// any "linux/amd64/<variant>" entry. Order is preserved relative to
// `wish`. Returns the same errors as BuildxPlatforms.
func FilterBuildxPlatforms(ctx context.Context, wish []string) ([]string, error) {
	var result []string
	err := sdkserve.Helper(ctx, "docker.FilterBuildxPlatforms", []any{wish}, &result)
	return result, err
}

// RunOptions configures a one-shot container invocation. The intended
// use is to execute a command in some language toolchain (npm, ruby,
// python, ...) without baking that toolchain into the runner image.
//
// Cross-environment safe: never relies on bind mounts, so it works
// under DinD. Inputs and outputs travel over the docker API via
// `docker cp`.
type RunOptions struct {
	// Image is the container image (e.g. "node:22-alpine"). Required.
	Image string `json:"image"`
	// Cmd is the command + args to run inside the container. When
	// empty, the image's default CMD runs.
	Cmd []string `json:"cmd"`
	// WorkDir is the working directory inside the container. Defaults
	// to "/work". InputDir's contents land here when InputDir is set.
	WorkDir string `json:"workDir"`
	// Env adds environment variables to the container.
	Env map[string]string `json:"env"`
	// User overrides the container user ("uid:gid" or "uid"). Empty
	// uses the image's default user. Named cache volumes often
	// require the image's default user to write, so override with
	// care.
	User string `json:"user"`
	// InputDir is a local directory whose contents are copied into the
	// container at WorkDir before Cmd runs. Empty means no inputs are
	// copied (the container starts with whatever the image provides).
	InputDir string `json:"inputDir"`
	// OutputDir is a path inside the container whose contents are
	// extracted after Cmd exits successfully. Empty means no outputs
	// are pulled. If the path does not exist after Cmd, Run returns an
	// error rather than silently producing an empty OutputTo.
	OutputDir string `json:"outputDir"`
	// OutputTo is the local destination directory for OutputDir's
	// contents. Created if missing. Required when OutputDir is set.
	// Existing files at OutputTo are NOT cleared first; the caller
	// owns staging.
	OutputTo string `json:"outputTo"`
	// Volumes maps named docker volumes to mount paths inside the
	// container, e.g. {"sparkwing-npm-cache": "/root/.npm"}. Use named
	// volumes (not host paths) so the same code works under DinD.
	Volumes map[string]string `json:"volumes"`
	// Stdout / Stderr receive the container's output during start.
	// Default os.Stdout / os.Stderr.
	Stdout io.Writer `json:"-"`
	Stderr io.Writer `json:"-"`
}

// Run executes Opts.Cmd inside a one-shot Opts.Image container. The
// flow is:
//
//  1. docker pull <image>          (checks the registry on every call)
//  2. docker create ...            (named container, returns id)
//  3. docker cp <inputDir>/. <id>:<workDir>/   (when InputDir set)
//  4. docker start -a <id>         (streams stdout/stderr)
//  5. docker cp <id>:<outputDir>/. <outputTo>/ (when OutputDir set)
//  6. docker rm <id>               (always; even on error)
//
// The docker daemon at DOCKER_HOST need not share a filesystem with
// the caller; all I/O moves through the docker API.
func Run(ctx context.Context, opts RunOptions) error {
	var out struct{ Stdout, Stderr []byte }
	err := sdkserve.Helper(ctx, "docker.Run", []any{struct {
		RunOptions
		CaptureOutput bool `json:"captureOutput"`
	}{opts, opts.Stdout != nil || opts.Stderr != nil}}, &out)
	stdout, stderr := opts.Stdout, opts.Stderr
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	_, outErr := stdout.Write(out.Stdout)
	_, errErr := stderr.Write(out.Stderr)
	return errors.Join(err, outErr, errErr)
}

// ImageTag holds the deterministic image-tag components for a single
// build: short commit SHA, content hash of the build's fileset, and a
// dirty bit. The same tag format is consumed by every deploy path
// (kind, gitops, ECR) so a tag's shape alone identifies the build
// inputs.
//
// Branch is a separate readable label; it isn't part of DeployTag /
// ProdTag (those stay content-addressed).
type ImageTag struct {
	Commit  string
	Content string `json:"content"`
	Branch  string
	Dirty   bool
}

// DeployTag is the canonical content-addressed tag applied to every
// build. Format: commit-<sha>-files-<hash>[-dirty], so a tag names the
// commit, the content hash and whether the tree was dirty; a clean commit
// always produces the same tag across re-runs.
func (t ImageTag) DeployTag() string {
	tag := "commit-" + t.Commit + "-files-" + t.Content
	if t.Dirty {
		tag += "-dirty"
	}
	return tag
}

// ProdTag is the gitops-consumed tag: DeployTag with a "-prod"
// suffix, so kind and prod gitops flows don't collide on the same
// digest in image-hash bookkeeping.
func (t ImageTag) ProdTag() string {
	return t.DeployTag() + "-prod"
}

// All returns every tag a build pipeline should apply: DeployTag plus
// ProdTag. Pushing both lets kind callers resolve by DeployTag and
// prod gitops resolve by ProdTag without re-tagging at deploy time.
func (t ImageTag) All() []string {
	return []string{t.DeployTag(), t.ProdTag()}
}

// ComputeTags reads the git repo state at the process CWD and
// returns an ImageTag describing it. Not running inside a git repo
// is a hard error. Pipelines that operate on a sibling clone use
// ComputeTagsIn instead.
func ComputeTags(ctx context.Context) (ImageTag, error) {
	var result ImageTag
	err := sdkserve.Helper(ctx, "docker.ComputeTags", []any{}, &result)
	return result, err
}

// ComputeTagsIn reads the git repo state in repoDir and returns an
// ImageTag describing it. Empty repoDir falls back to process CWD.
func ComputeTagsIn(ctx context.Context, repoDir string) (ImageTag, error) {
	var result ImageTag
	err := sdkserve.Helper(ctx, "docker.ComputeTagsIn", []any{repoDir}, &result)
	return result, err
}
