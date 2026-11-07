package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

type environment struct {
	dir string
	env []string
}
type key struct{}

// WithEnvironment binds helper commands to the requesting pipeline process.
func WithEnvironment(ctx context.Context, dir string, env []string) context.Context {
	return context.WithValue(ctx, key{}, environment{dir, env})
}

// Command constructs a helper command through the host environment policy.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	e, ok := ctx.Value(key{}).(environment)
	if !ok {
		e.env = os.Environ()
	}
	path, err := procenv.LookupCommand(name, e.env, e.dir)
	if err != nil {
		cmd := procenv.SDKCommandContext(ctx, e.env, name, args...)
		cmd.Err = err
		return cmd
	}
	command := procenv.SDKCommandContext(ctx, e.env, path, args...)
	command.Dir = e.dir
	return command
}

// LookPath resolves a helper executable in the pipeline's environment.
func LookPath(ctx context.Context, name string) (string, error) {
	e, ok := ctx.Value(key{}).(environment)
	if !ok {
		e.env = os.Environ()
		dir, err := os.Getwd()
		if err != nil {
			return "", err
		}
		e.dir = dir
	}
	return procenv.LookupCommand(name, e.env, e.dir)
}

type (
	cleanupKey struct{}
	cleanups   struct {
		register func([]string, string) (string, error)
		release  func(context.Context, string) error
	}
)

// WithCleanups binds resources to the host's process cleanup ledger.
func WithCleanups(ctx context.Context, register func([]string, string) (string, error), release func(context.Context, string) error) context.Context {
	return context.WithValue(ctx, cleanupKey{}, cleanups{register, release})
}

// Register records cleanup before a helper creates a resource.
func Register(ctx context.Context, argv []string, description string) (string, error) {
	c, ok := ctx.Value(cleanupKey{}).(cleanups)
	if !ok {
		return "", fmt.Errorf("host cleanup service unavailable")
	}
	return c.register(argv, description)
}

// Release executes and clears a process-owned cleanup.
func Release(ctx context.Context, id string) error {
	c, ok := ctx.Value(cleanupKey{}).(cleanups)
	if !ok {
		return fmt.Errorf("host cleanup service unavailable")
	}
	return c.release(ctx, id)
}

// Path resolves a helper filesystem input in the pipeline process's working directory.
func Path(ctx context.Context, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	e, ok := ctx.Value(key{}).(environment)
	if !ok {
		return path
	}
	return filepath.Join(e.dir, path)
}

// Environment returns the captured working directory and command environment.
func Environment(ctx context.Context) (string, []string) {
	e, ok := ctx.Value(key{}).(environment)
	if !ok {
		if dir, err := os.Getwd(); err == nil {
			e.dir = dir
		}
		e.env = os.Environ()
	}
	return e.dir, e.env
}

type (
	outputKey struct{}
	output    struct{ stdout, stderr io.Writer }
)

// WithOutput binds default helper output to the host's masked log streams.
func WithOutput(ctx context.Context, stdout, stderr io.Writer) context.Context {
	return context.WithValue(ctx, outputKey{}, output{stdout, stderr})
}

// Output returns the host-owned default streams for this helper.
func Output(ctx context.Context) (io.Writer, io.Writer) {
	streams, _ := ctx.Value(outputKey{}).(output)
	return streams.stdout, streams.stderr
}
