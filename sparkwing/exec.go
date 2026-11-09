package sparkwing

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	sdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"
)

// ExitNotStarted marks an ExecError where the child process never
// ran (chdir failure, ENOENT-on-binary, pipe setup error).
const ExitNotStarted = -1

// WorkDir returns the pipeline working directory (the repo root).
func WorkDir() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return dir
}

// ExecResult is the structured result of a command invocation.
type ExecResult struct {
	Command  string
	Stdout   string
	Stderr   string
	ExitCode int
}

// ExecError is returned when a command exits non-zero. The unstructured
// output is bundled so failure messages are self-contained without
// re-reading logs.
type ExecError struct {
	Command  string
	Stdout   string
	Stderr   string
	ExitCode int
	Cause    error
	// Terminated names why a started process was killed rather than
	// exiting on its own: "timeout" when its context deadline expired,
	// "cancellation" when the context was cancelled,
	// "signal" for any other kill. Empty for a normal exit or a process
	// that never started. A signalled process reports ExitCode -1, which
	// collides with ExitNotStarted, so this field -- not ExitCode -- is
	// what tells a kill apart from a failed launch.
	Terminated string
}

const execErrorCommandBytes = 512

// Error repeats at most 512 bytes of the command and counts the rest, so a
// generated argument list cannot bury the failure; Command keeps it whole.
func (e *ExecError) Error() string {
	var b strings.Builder
	command := e.Command
	if len(command) > execErrorCommandBytes {
		cut := execErrorCommandBytes
		for cut > 0 && !utf8.RuneStart(command[cut]) {
			cut--
		}
		command = fmt.Sprintf("%s... [%d more bytes]", command[:cut], len(e.Command)-cut)
	}
	if e.Terminated != "" {
		if e.Terminated == "timeout" {
			fmt.Fprintf(&b, "command timed out: %s", command)
		} else {
			fmt.Fprintf(&b, "command terminated by %s: %s", e.Terminated, command)
		}
		if e.Cause != nil {
			fmt.Fprintf(&b, " (%v)", e.Cause)
		}
		if e.Terminated == "timeout" {
			b.WriteString("; increase the context timeout or make the command faster")
		}
		return b.String()
	}
	if e.ExitCode == ExitNotStarted {
		fmt.Fprintf(&b, "command failed to start: %s", command)
		if e.Cause != nil {
			fmt.Fprintf(&b, ": %v", e.Cause)
		}
		return b.String()
	}
	fmt.Fprintf(&b, "command failed (exit %d): %s", e.ExitCode, command)
	out := strings.TrimSpace(e.Stderr)
	if out == "" {
		out = strings.TrimSpace(e.Stdout)
	}
	if out != "" {
		fmt.Fprintf(&b, "\n%s", out)
	}
	return b.String()
}

func (e *ExecError) Unwrap() error { return e.Cause }

type cmdKind int

const (
	kindBash cmdKind = iota
	kindExec
)

// Cmd is the chainable command builder returned by Bash and Exec.
// Modifiers (Dir, Env, EnvMap) return *Cmd so calls compose; terminators
// (Run, Capture, String, Lines, JSON, MustBeEmpty) actually execute.
//
//	sparkwing.Bash(ctx, "go test ./...").Run()
//	sparkwing.Bash(ctx, `git -C "$D" diff --name-only`).Env("D", repoDir).MustBeEmpty("uncommitted changes")
//	out, _ := sparkwing.Exec(ctx, "git", "rev-parse", "HEAD").String()
type Cmd struct {
	ctx  context.Context
	kind cmdKind

	line string
	name string
	args []string
	dir  string
	env  map[string]string
}

type commandEnvContextKey struct{}

// WithCommandEnv returns a context whose sparkwing.Exec/Bash calls inherit env.
// Explicit Cmd.Env/Cmd.EnvMap values still win for the same key.
func WithCommandEnv(ctx context.Context, env map[string]string) context.Context {
	if len(env) == 0 {
		return ctx
	}
	merged := map[string]string{}
	if existing, ok := ctx.Value(commandEnvContextKey{}).(map[string]string); ok {
		for key, value := range existing {
			merged[key] = value
		}
	}
	for key, value := range env {
		if key != "" {
			merged[key] = value
		}
	}
	return context.WithValue(ctx, commandEnvContextKey{}, merged)
}

func commandEnvFromContext(ctx context.Context) map[string]string {
	env, _ := ctx.Value(commandEnvContextKey{}).(map[string]string)
	return env
}

// Bash starts building a shell command (run via "bash -c"). The line
// is the shell program verbatim -- there is no printf-style formatting,
// so dynamic values must come through .Env() (the shell expands the
// var safely) or argv via Exec. Splicing dynamic values into a shell
// string is a quoting/injection footgun.
//
// Runs in WorkDir() unless redirected via .Dir(). Terminate the chain
// with .Run() (stream output) or .Capture() / .String() / .Lines() /
// .JSON() / .MustBeEmpty() (silent, post-exec parse).
//
//	sparkwing.Bash(ctx, "go vet ./...").Run()
//	sparkwing.Bash(ctx, `git -C "$R" status --porcelain`).Env("R", repo).MustBeEmpty("dirty tree")
//	sparkwing.Bash(ctx, `if [[ -d .git ]]; then echo repo; fi`).Run()
//
// Prefer [Exec] for argv-shaped invocations. No shell parsing means no
// quoting concerns and safer handling of values that might contain
// shell metacharacters (spaces, $, backticks). Reserve Bash for cases
// that genuinely need shell features (pipes, redirects, globs,
// conditionals).
//
// Signal propagation: the child runs in its own process group. Cancellation
// force-kills the whole group, so grandchildren do not outlive a cancelled run.
// On Linux and macOS, a no-progress timeout first requests a goroutine dump. A
// process that remains alive after the diagnostic window is force-killed.
func Bash(ctx context.Context, line string) *Cmd {
	return &Cmd{ctx: ctx, kind: kindBash, line: line}
}

// Exec starts building an argv command (no shell). Use this whenever
// you have argv-shaped inputs, especially anything dynamic -- there is
// no shell, so no quoting and no injection risk.
//
//	sparkwing.Exec(ctx, "go", "test", "./...").Dir("internal").Run()
//	sparkwing.Exec(ctx, "kubectl", "apply", "-f", manifestPath).Run()
//	sparkwing.Exec(ctx, "docker", "push", tag).Run()
//
// Signal propagation: the binary runs in its own process group. Cancellation
// force-kills the whole group, so grandchildren do not outlive a cancelled run.
// On Linux and macOS, a no-progress timeout first requests a goroutine dump. A
// process that remains alive after the diagnostic window is force-killed.
func Exec(ctx context.Context, name string, args ...string) *Cmd {
	return &Cmd{ctx: ctx, kind: kindExec, name: name, args: args}
}

// Dir sets the working directory for the command. Relative dir is
// resolved against WorkDir() (the pipeline root), matching
// `working-directory:` in GitHub Actions and `WORKDIR` in
// Dockerfiles. Absolute dir is used as-is.
func (c *Cmd) Dir(path string) *Cmd {
	c.dir = path
	return c
}

// Env adds (or overrides) a single environment variable.
func (c *Cmd) Env(key, value string) *Cmd {
	if c.env == nil {
		c.env = make(map[string]string, 1)
	}
	c.env[key] = value
	return c
}

// EnvMap merges a map of environment variables into the command.
// Existing keys are overwritten; the host environment is preserved.
func (c *Cmd) EnvMap(env map[string]string) *Cmd {
	if len(env) == 0 {
		return c
	}
	if c.env == nil {
		c.env = make(map[string]string, len(env))
	}
	for k, v := range env {
		c.env[k] = v
	}
	return c
}

// Run executes the command, forwarding stdout/stderr bytes to
// the logger installed in ctx. The host masks and splits them into lines. Returns an ExecResult on success, or
// an *ExecError on non-zero exit / failure to start. The result keeps only
// the last 64 KiB of each masked stream; use
// Capture for the whole output.
func (c *Cmd) Run() (ExecResult, error) {
	return c.execute(false)
}

// Capture executes the command silently -- no output log records,
// just the exec_start banner. The full stdout/stderr are returned in
// the ExecResult; on failure, the *ExecError carries the captured
// streams.
//
// Use Capture for value-fetch invocations whose chatter would bury
// the real build output.
func (c *Cmd) Capture() (ExecResult, error) {
	return c.execute(true)
}

// String runs the command silently and returns TrimSpace(stdout).
// Common shape for "git rev-parse HEAD" / "go env GOBIN" reads.
func (c *Cmd) String() (string, error) {
	res, err := c.execute(true)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}

// Lines runs the command silently and returns stdout split on "\n",
// with each line trimmed and blanks dropped. Common shape for
// "go list ./..." / "git ls-files" iteration.
func (c *Cmd) Lines() ([]string, error) {
	res, err := c.execute(true)
	if err != nil {
		return nil, err
	}
	raw := strings.Split(res.Stdout, "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

// JSON runs the command silently and decodes stdout into out via
// encoding/json. *ExecError is preserved on non-zero exit; only the
// parse step adds a wrapping error.
func (c *Cmd) JSON(out any) error {
	res, err := c.execute(true)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(res.Stdout), out); err != nil {
		return fmt.Errorf("parse JSON from %q: %w", res.Command, err)
	}
	return nil
}

// MustBeEmpty runs the command silently and returns nil only if its
// stdout (after TrimSpace) is empty. Non-empty output is reported as
// reason + the offending stdout. Common shape for drift checks like
// `gofmt -l` or `git diff --name-only`.
func (c *Cmd) MustBeEmpty(reason string) error {
	res, err := c.execute(true)
	if err != nil {
		return err
	}
	if out := strings.TrimSpace(res.Stdout); out != "" {
		return fmt.Errorf("%s:\n%s", reason, out)
	}
	return nil
}

func (c *Cmd) execute(silent bool) (ExecResult, error) {
	var helper string
	switch c.kind {
	case kindBash:
		helper = "sparkwing.Bash"
	case kindExec:
		helper = "sparkwing.Exec"
	}
	sdkserve.RequireExecution(c.ctx, helper)

	var name string
	var args []string
	switch c.kind {
	case kindBash:
		name, args = "bash", []string{"-c", c.line}
	case kindExec:
		name, args = c.name, c.args
	}
	dir := c.dir
	if dir == "" {
		dir = WorkDir()
	}
	ctx := c.ctx
	if silent {
		ctx = withSilent(ctx)
	}
	return execCmd(ctx, name, args, dir, mergeCommandEnv(commandEnvFromContext(ctx), c.env))
}

func mergeCommandEnv(contextEnv, explicitEnv map[string]string) map[string]string {
	if len(contextEnv) == 0 && len(explicitEnv) == 0 {
		return nil
	}
	merged := make(map[string]string, len(contextEnv)+len(explicitEnv))
	for key, value := range contextEnv {
		merged[key] = value
	}
	for key, value := range explicitEnv {
		merged[key] = value
	}
	return merged
}

type silentKey struct{}

func withSilent(ctx context.Context) context.Context {
	return context.WithValue(ctx, silentKey{}, true)
}

func isSilent(ctx context.Context) bool {
	v, _ := ctx.Value(silentKey{}).(bool)
	return v
}

func execCmd(ctx context.Context, name string, args []string, dir string, extraEnv map[string]string) (ExecResult, error) {
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(WorkDir(), dir)
	}
	return executeCommand(ctx, name, args, dir, extraEnv)
}

func renderCommand(name string, args []string) string {
	if name == "bash" && len(args) == 2 && args[0] == "-c" {
		return args[1]
	}
	parts := append([]string{name}, args...)
	return strings.Join(parts, " ")
}
