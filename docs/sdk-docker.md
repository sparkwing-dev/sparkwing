<!-- GENERATED from the `sparkwing` package via go/doc (internal/sdkref). Do not edit by hand; regenerate with `bash bin/gen-sdk-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# SDK API reference: `sparkwing/docker`

Package docker is the sparkwing SDK's Docker-shelling helper layer: build, push, login, and deterministic tag computation.

Import as `swdocker "github.com/sparkwing-dev/sparkwing/sparkwing/docker"`. The root package and the other subpackages are indexed in [sdk-reference.md](sdk-reference.md).

## Functions

- `func BuildxPlatforms(ctx context.Context) ([]string, error)` -- BuildxPlatforms returns the list of platforms the active buildx builder advertises (e.g.
- `func FilterBuildxPlatforms(ctx context.Context, wish []string) ([]string, error)` -- FilterBuildxPlatforms returns the subset of `wish` that the active buildx builder can build.
- `func Login(ctx context.Context, registry, username, secret string) error` -- Login authenticates docker with the given registry.
- `func Push(ctx context.Context, image string, tags, registries []string) error` -- Push tags and pushes the given local `image` reference to each registry with every requested tag.
- `func Run(ctx context.Context, opts RunOptions) error` -- Run executes Opts.Cmd inside a one-shot Opts.Image container.

## Types

### type BuildConfig

BuildConfig configures a Docker image build and optional push.

```
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
```


### type BuildResult

BuildResult is returned by Build / BuildAndPush.

```
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
```

- `func Build(ctx context.Context, cfg BuildConfig) (BuildResult, error)` -- Build runs `docker build` (or `docker buildx build` when Platforms is non-empty) and applies the tags locally.
- `func BuildAndPush(ctx context.Context, cfg BuildConfig) (BuildResult, error)` -- BuildAndPush builds the image (same rules as Build) and pushes it to every configured registry.

### type ImageTag

ImageTag holds the deterministic image-tag components for a single build: short commit SHA, content hash of the build's fileset, and a dirty bit.

```
type ImageTag struct {
    Commit  string
    Content string
    Branch  string
    Dirty   bool
}
```

- `func ComputeTags(ctx context.Context) (ImageTag, error)` -- ComputeTags reads the git repo state at the process CWD and returns an ImageTag describing it.
- `func ComputeTagsIn(ctx context.Context, repoDir string) (ImageTag, error)` -- ComputeTagsIn reads the git repo state in repoDir and returns an ImageTag describing it.
- `func (t ImageTag) All() []string` -- All returns every tag a build pipeline should apply: DeployTag plus ProdTag.
- `func (t ImageTag) DeployTag() string` -- DeployTag is the canonical content-addressed tag applied to every build.
- `func (t ImageTag) ProdTag() string` -- ProdTag is the gitops-consumed tag: DeployTag with a "-prod" suffix, so kind and prod gitops flows don't collide on the same digest in image-hash bookkeeping.

### type RunOptions

RunOptions configures a one-shot container invocation.

```
type RunOptions struct {
    // Image is the container image (e.g. "node:22-alpine"). Required.
    Image string

    // Cmd is the command + args to run inside the container. When
    // empty, the image's default CMD runs.
    Cmd []string

    // WorkDir is the working directory inside the container. Defaults
    // to "/work". InputDir's contents land here when InputDir is set.
    WorkDir string

    // Env adds environment variables to the container.
    Env map[string]string

    // User overrides the container user ("uid:gid" or "uid"). Empty
    // uses the image's default user. Named cache volumes often
    // require the image's default user to write, so override with
    // care.
    User string

    // InputDir is a local directory whose contents are copied into the
    // container at WorkDir before Cmd runs. Empty means no inputs are
    // copied (the container starts with whatever the image provides).
    InputDir string

    // OutputDir is a path inside the container whose contents are
    // extracted after Cmd exits successfully. Empty means no outputs
    // are pulled. If the path does not exist after Cmd, Run returns an
    // error rather than silently producing an empty OutputTo.
    OutputDir string

    // OutputTo is the local destination directory for OutputDir's
    // contents. Created if missing. Required when OutputDir is set.
    // Existing files at OutputTo are NOT cleared first; the caller
    // owns staging.
    OutputTo string

    // Volumes maps named docker volumes to mount paths inside the
    // container, e.g. {"sparkwing-npm-cache": "/root/.npm"}. Use named
    // volumes (not host paths) so the same code works under DinD.
    Volumes map[string]string

    // Stdout / Stderr receive the container's output during start.
    // Default os.Stdout / os.Stderr.
    Stdout io.Writer
    Stderr io.Writer
}
```


## Variables

```
var ErrBuildxRequired = errors.New("docker: buildx plugin required for multi-platform builds")
```

```
var ErrDockerUnavailable = errors.New("docker: binary not available on PATH")
```

```
var ErrPlatformUnsupported = errors.New("docker: requested platform not supported by active buildx builder")
```
