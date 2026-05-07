package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"

	"github.com/sparkwing-dev/sparkwing/v2/sparkwing/planguard"
)

// RunOptions configures a one-shot container invocation. The intended
// use is to execute a command in some language toolchain (npm, ruby,
// python, ...) without baking that toolchain into the runner image.
//
// Cross-environment safe: never relies on bind mounts, so it works
// under DinD. Inputs and outputs travel over the docker API via
// `docker cp`.
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

// Run executes Opts.Cmd inside a one-shot Opts.Image container. The
// flow is:
//
//  1. docker pull <image>          (idempotent; cached after first run)
//  2. docker create ...            (named container, returns id)
//  3. docker cp <inputDir>/. <id>:<workDir>/   (when InputDir set)
//  4. docker start -a <id>         (streams stdout/stderr)
//  5. docker cp <id>:<outputDir>/. <outputTo>/ (when OutputDir set)
//  6. docker rm <id>               (always; even on error)
//
// The docker daemon at DOCKER_HOST need not share a filesystem with
// the caller; all I/O moves through the docker API.
func Run(ctx context.Context, opts RunOptions) error {
	planguard.Guard(ctx, "docker.Run")
	if err := ensureDocker(); err != nil {
		return err
	}
	if opts.Image == "" {
		return errors.New("docker.Run: Image is required")
	}
	if opts.OutputDir != "" && opts.OutputTo == "" {
		return errors.New("docker.Run: OutputTo is required when OutputDir is set")
	}
	if opts.WorkDir == "" {
		opts.WorkDir = "/work"
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	if _, err := runDocker(ctx, nil, "pull", opts.Image); err != nil {
		return fmt.Errorf("docker pull %s: %w", opts.Image, err)
	}

	name := "sparkwing-run-" + randomSuffix()
	createArgs := []string{"create", "--name", name, "-w", opts.WorkDir}
	if opts.User != "" {
		createArgs = append(createArgs, "--user", opts.User)
	}
	for _, k := range sortedKeys(opts.Env) {
		createArgs = append(createArgs, "-e", k+"="+opts.Env[k])
	}
	for _, vol := range sortedKeys(opts.Volumes) {
		createArgs = append(createArgs, "-v", vol+":"+opts.Volumes[vol])
	}
	createArgs = append(createArgs, opts.Image)
	createArgs = append(createArgs, opts.Cmd...)

	if _, err := runDocker(ctx, nil, createArgs...); err != nil {
		return fmt.Errorf("docker create: %w", err)
	}
	defer func() {
		_, _ = runDocker(context.Background(), nil, "rm", "-f", name)
	}()

	if opts.InputDir != "" {
		// Trailing /. copies directory contents into WorkDir;
		// matches `cp -r src/. dst/` semantics.
		if _, err := runDocker(ctx, nil, "cp", opts.InputDir+"/.", name+":"+opts.WorkDir); err != nil {
			return fmt.Errorf("docker cp inputs into %s: %w", name, err)
		}
	}

	startCmd := exec.CommandContext(ctx, "docker", "start", "-a", name)
	startCmd.Stdout = opts.Stdout
	startCmd.Stderr = opts.Stderr
	if err := startCmd.Run(); err != nil {
		return fmt.Errorf("docker start %s: %w", name, err)
	}

	if opts.OutputDir != "" {
		if err := os.MkdirAll(opts.OutputTo, 0o755); err != nil {
			return fmt.Errorf("create OutputTo %s: %w", opts.OutputTo, err)
		}
		if _, err := runDocker(ctx, nil, "cp", name+":"+opts.OutputDir+"/.", opts.OutputTo); err != nil {
			return fmt.Errorf("docker cp outputs from %s: %w", name, err)
		}
	}

	return nil
}

func randomSuffix() string {
	var buf [6]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
