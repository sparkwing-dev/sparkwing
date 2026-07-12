package sparkwing

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

// ExitNotStarted marks an ExecError where the child process never
// ran (chdir failure, ENOENT-on-binary, pipe setup error).
const ExitNotStarted = -1

// WorkDir returns the pipeline working directory (the repo root).
func WorkDir() string { return runtime.WorkDir }

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
	// exiting on its own: "cancellation" when the run's context ended,
	// "signal" for any other kill. Empty for a normal exit or a process
	// that never started. A signalled process reports ExitCode -1, which
	// collides with ExitNotStarted, so this field -- not ExitCode -- is
	// what tells a kill apart from a failed launch.
	Terminated string
}

func (e *ExecError) Error() string {
	var b strings.Builder
	if e.Terminated != "" {
		fmt.Fprintf(&b, "command terminated by %s: %s", e.Terminated, e.Command)
		if e.Cause != nil {
			fmt.Fprintf(&b, " (%v)", e.Cause)
		}
		return b.String()
	}
	if e.ExitCode == ExitNotStarted {
		fmt.Fprintf(&b, "command failed to start: %s", e.Command)
		if e.Cause != nil {
			fmt.Fprintf(&b, ": %v", e.Cause)
		}
		return b.String()
	}
	fmt.Fprintf(&b, "command failed (exit %d): %s", e.ExitCode, e.Command)
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

// cmdKind selects the executor used for a Cmd: bash or argv exec.
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
	// For kindBash, line holds the (already-formatted) command line.
	// For kindExec, name + args hold the argv.
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
// Signal propagation: the child runs in its own process group, and ctx
// cancellation SIGKILLs the whole group. A bash pipeline that forks
// further -- backgrounded work, a wrapped long-lived helper -- is torn
// down with it, so grandchildren do not outlive a cancelled run. Terminal
// SIGINT (Ctrl-C) also reaches the group via the OS.
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
// Signal propagation: the binary runs in its own process group, and ctx
// cancellation SIGKILLs the whole group. Single-process CLIs (go, kubectl,
// docker, git) terminate cleanly; a binary that forks long-lived children
// has them torn down too, since the group -- not just the direct child --
// is signalled. Terminal SIGINT (Ctrl-C) reaches the group via the OS.
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

// Run executes the command, streaming stdout/stderr line-by-line to
// the logger installed in ctx. Returns an ExecResult on success, or
// an *ExecError on non-zero exit / failure to start.
func (c *Cmd) Run() (ExecResult, error) {
	return c.execute(false)
}

// Capture executes the command silently -- no per-line log records,
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
	planguard.Guard(c.ctx, helper)

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
	display := renderCommand(name, args)
	if dir == "" {
		return ExecResult{Command: display}, &ExecError{
			Command:  display,
			ExitCode: ExitNotStarted,
			Cause:    fmt.Errorf("%w (cannot run %q without a project root)", ErrNoProject, name),
		}
	}
	if !filepath.IsAbs(dir) {
		wd := WorkDir()
		if wd == "" {
			return ExecResult{Command: display}, &ExecError{
				Command:  display,
				ExitCode: ExitNotStarted,
				Cause:    fmt.Errorf("%w (cannot resolve relative dir %q)", ErrNoProject, dir),
			}
		}
		dir = filepath.Join(wd, dir)
	}
	if dir != "" {
		if info, statErr := os.Stat(dir); statErr != nil {
			return ExecResult{Command: display}, &ExecError{
				Command:  display,
				ExitCode: ExitNotStarted,
				Cause:    fmt.Errorf("dir %q: %w", dir, statErr),
			}
		} else if !info.IsDir() {
			return ExecResult{Command: display}, &ExecError{
				Command:  display,
				ExitCode: ExitNotStarted,
				Cause:    fmt.Errorf("dir %q: not a directory", dir),
			}
		}
	}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = os.Environ()
		for k, v := range extraEnv {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	configureProcessGroup(cmd)

	logger := LoggerFromContext(ctx)
	logger.Emit(recordEnvelope(ctx, LogRecord{
		TS:    time.Now(),
		Level: "info",
		JobID: NodeFromContext(ctx),
		Event: "exec_start",
		Msg:   "$ " + display,
	}))
	Debug(ctx, "exec: %s (dir=%s)", display, dir)
	if len(extraEnv) > 0 && DebugEnabled() {
		Debug(ctx, "exec env: %s", formatEnvDiff(extraEnv))
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ExecResult{Command: display}, &ExecError{Command: display, ExitCode: ExitNotStarted, Cause: err}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return ExecResult{Command: display}, &ExecError{Command: display, ExitCode: ExitNotStarted, Cause: err}
	}

	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		return ExecResult{Command: display}, &ExecError{Command: display, ExitCode: ExitNotStarted, Cause: err}
	}

	var outBuf, errBuf strings.Builder
	var wg sync.WaitGroup
	wg.Add(2)
	go streamLines(ctx, &wg, stdout, "info", logger, &outBuf)
	go streamLines(ctx, &wg, stderr, "info", logger, &errBuf)

	waitErr := cmd.Wait()
	wall := time.Since(startedAt)
	wg.Wait()

	emitCommandResources(ctx, cmd, wall)

	res := ExecResult{
		Command:  display,
		Stdout:   outBuf.String(),
		Stderr:   errBuf.String(),
		ExitCode: cmd.ProcessState.ExitCode(),
	}
	Debug(ctx, "exec done: exit=%d bytes_stdout=%d bytes_stderr=%d",
		res.ExitCode, len(res.Stdout), len(res.Stderr))
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			return res, &ExecError{
				Command:    display,
				Stdout:     res.Stdout,
				Stderr:     res.Stderr,
				ExitCode:   res.ExitCode,
				Cause:      waitErr,
				Terminated: terminationReason(ctx, ee),
			}
		}
		return res, &ExecError{
			Command:  display,
			ExitCode: ExitNotStarted,
			Cause:    waitErr,
		}
	}
	return res, nil
}

// terminationReason classifies a process that its ExitError says was
// killed rather than exited: "cancellation" when the run's context has
// ended (exec.CommandContext SIGKILLed the child on teardown), "signal"
// for any other kill (an external SIGKILL, an OOM). It returns "" for a
// process that exited on its own, so a genuine non-zero exit keeps the
// "command failed (exit N)" wording.
func terminationReason(ctx context.Context, ee *exec.ExitError) string {
	if ee.Exited() {
		return ""
	}
	if ctx.Err() != nil {
		return "cancellation"
	}
	return "signal"
}

// emitCommandResources measures the finished command's CPU and peak memory
// from its wait4 rusage and reports them to the node's resource reporter, so
// subprocess cost lands in the run's measured profile. cpu/wall gives the
// command's average core draw over its span; wall is the command's real
// duration, so a subtree that ran for many seconds is not mistaken for a
// same-cost burst. Best-effort: a missing rusage or reporter is a no-op.
func emitCommandResources(ctx context.Context, cmd *exec.Cmd, wall time.Duration) {
	cpu, maxRSS, ok := commandResourceUsage(cmd)
	if !ok {
		return
	}
	var millicores int64
	if wall > 0 {
		millicores = int64(cpu.Seconds() / wall.Seconds() * 1000.0)
	}
	if millicores < 0 {
		millicores = 0
	}
	reportResource(ctx, ResourceSample{CPUMillicores: millicores, MemoryBytes: maxRSS, CPUTime: cpu})
}

// streamLines reads r line-by-line, tees to buf, and pushes each line
// to the logger as an exec_line record.
func streamLines(ctx context.Context, wg *sync.WaitGroup, r io.ReadCloser, level string, logger Logger, buf *strings.Builder) {
	defer wg.Done()
	defer r.Close()
	node := NodeFromContext(ctx)
	silent := isSilent(ctx)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		buf.WriteString(line)
		buf.WriteByte('\n')
		if silent {
			continue
		}
		logger.Emit(recordEnvelope(ctx, LogRecord{
			TS:    time.Now(),
			Level: level,
			JobID: node,
			Event: "exec_line",
			Msg:   line,
		}))
	}
}

// renderCommand produces a single-line display of the command. No
// quoting; good enough for log banners.
func renderCommand(name string, args []string) string {
	if name == "bash" && len(args) == 2 && args[0] == "-c" {
		return args[1]
	}
	parts := append([]string{name}, args...)
	return strings.Join(parts, " ")
}

// formatEnvDiff renders extra env vars as a stable, sorted KEY=VALUE
// list. Values pass through the logger's Masker, so any registered
// Secret value renders as `***`.
func formatEnvDiff(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(env[k])
	}
	return b.String()
}
