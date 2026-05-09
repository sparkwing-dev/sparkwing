package sparkwing_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestSh_Success(t *testing.T) {
	logger := &recordingLogger{}
	ctx := sparkwing.WithLogger(context.Background(), logger)

	res, err := sparkwing.Bash(ctx, "echo hello-world").Run()
	if err != nil {
		t.Fatalf("Sh: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "hello-world") {
		t.Fatalf("Stdout missing echo: %q", res.Stdout)
	}
	// Logger should have received the output line.
	if len(logger.lines) == 0 {
		t.Fatal("logger saw no lines")
	}
}

func TestSh_FailureProducesExecError(t *testing.T) {
	ctx := sparkwing.WithLogger(context.Background(), &recordingLogger{})
	_, err := sparkwing.Bash(ctx, "exit 7").Run()
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	var ee *sparkwing.ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("error is not *ExecError: %T", err)
	}
	if ee.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", ee.ExitCode)
	}
}

func TestCmd_DirRunsInDir(t *testing.T) {
	dir := t.TempDir()
	res, err := sparkwing.Bash(context.Background(), "pwd").Dir(dir).Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(strings.TrimSpace(res.Stdout), dir) {
		t.Fatalf("pwd = %q, should contain %q", res.Stdout, dir)
	}
}

func TestCmd_EnvMapInjectsEnv(t *testing.T) {
	res, err := sparkwing.Exec(context.Background(), "sh", "-c", "echo $SPARKWING_TEST_VAR").
		EnvMap(map[string]string{"SPARKWING_TEST_VAR": "xyz"}).
		Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "xyz") {
		t.Fatalf("env var not propagated: %q", res.Stdout)
	}
}

func TestCmd_EnvSingle(t *testing.T) {
	ctx := sparkwing.WithLogger(context.Background(), &recordingLogger{})
	res, err := sparkwing.Bash(ctx, "echo $SPARKWING_TEST_VAR").
		Env("SPARKWING_TEST_VAR", "shval").
		Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "shval") {
		t.Fatalf("env var not propagated: %q", res.Stdout)
	}
}

func TestBash_RunsBashOnlyFeatures(t *testing.T) {
	ctx := sparkwing.WithLogger(context.Background(), &recordingLogger{})
	// `[[ ... ]]` is bash-only; busybox sh / dash reject it.
	res, err := sparkwing.Bash(ctx, `if [[ "abc" == a* ]]; then echo matched; fi`).Run()
	if err != nil {
		t.Fatalf("Bash: %v", err)
	}
	if !strings.Contains(res.Stdout, "matched") {
		t.Fatalf("Bash output missing match: %q", res.Stdout)
	}
}

func TestBash_DirRunsInDir(t *testing.T) {
	dir := t.TempDir()
	res, err := sparkwing.Bash(context.Background(), "pwd").Dir(dir).Run()
	if err != nil {
		t.Fatalf("Bash: %v", err)
	}
	if !strings.Contains(strings.TrimSpace(res.Stdout), dir) {
		t.Fatalf("pwd = %q, should contain %q", res.Stdout, dir)
	}
}

func TestExecError_MessageIncludesCommandAndOutput(t *testing.T) {
	ctx := sparkwing.WithLogger(context.Background(), &recordingLogger{})
	_, err := sparkwing.Bash(ctx, "echo problem-one >&2 ; exit 1").Run()
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "exit 1") {
		t.Fatalf("error missing exit: %q", msg)
	}
	if !strings.Contains(msg, "problem-one") {
		t.Fatalf("error missing stderr context: %q", msg)
	}
}

func TestBash_EnvInjects(t *testing.T) {
	ctx := sparkwing.WithLogger(context.Background(), &recordingLogger{})
	// Use a bash-only construct to confirm we really ran under bash.
	res, err := sparkwing.Bash(ctx, `if [[ -n "$SPARKWING_TEST_VAR" ]]; then echo got-$SPARKWING_TEST_VAR; fi`).
		Env("SPARKWING_TEST_VAR", "bashval").
		Run()
	if err != nil {
		t.Fatalf("Bash: %v", err)
	}
	if !strings.Contains(res.Stdout, "got-bashval") {
		t.Fatalf("env var not propagated through bash: %q", res.Stdout)
	}
}

// When the dir does not exist, the renderer must NOT claim
// "exit 0". The cause must be visible in the human string.
func TestCmd_DirMissingRendersStartFailure(t *testing.T) {
	ctx := sparkwing.WithLogger(context.Background(), &recordingLogger{})
	bogus := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := sparkwing.Bash(ctx, "true").Dir(bogus).Run()
	if err == nil {
		t.Fatal("expected error for missing dir")
	}
	var ee *sparkwing.ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("error is not *ExecError: %T", err)
	}
	if ee.ExitCode != sparkwing.ExitNotStarted {
		t.Fatalf("ExitCode = %d, want ExitNotStarted (%d)", ee.ExitCode, sparkwing.ExitNotStarted)
	}
	msg := err.Error()
	if strings.Contains(msg, "exit 0") {
		t.Fatalf("error must not pretend exit 0 for chdir failure: %q", msg)
	}
	if !strings.Contains(msg, "command failed to start") {
		t.Fatalf("missing failed-to-start prefix: %q", msg)
	}
	if !strings.Contains(msg, "does-not-exist") {
		t.Fatalf("missing dir name in cause: %q", msg)
	}
}

// Missing-binary path (Exec with a name that's not on PATH)
// must surface the ENOENT cause, not "exit 0".
func TestExec_MissingBinaryRendersStartFailure(t *testing.T) {
	ctx := sparkwing.WithLogger(context.Background(), &recordingLogger{})
	_, err := sparkwing.Exec(ctx, "sparkwing-bogus-binary-xyz").Run()
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	var ee *sparkwing.ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("error is not *ExecError: %T", err)
	}
	if ee.ExitCode != sparkwing.ExitNotStarted {
		t.Fatalf("ExitCode = %d, want ExitNotStarted (%d)", ee.ExitCode, sparkwing.ExitNotStarted)
	}
	msg := err.Error()
	if strings.Contains(msg, "exit 0") {
		t.Fatalf("error must not pretend exit 0: %q", msg)
	}
	if !strings.Contains(msg, "command failed to start") {
		t.Fatalf("missing failed-to-start prefix: %q", msg)
	}
}

// A relative dir is resolved against WorkDir(), not the
// runner-process cwd. .Dir("sub") from a pipeline rooted at /tmp/foo
// must run in /tmp/foo/sub.
func TestCmd_RelativeDirResolvesAgainstWorkDir(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	prev := sparkwing.WorkDir()
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })

	ctx := sparkwing.WithLogger(context.Background(), &recordingLogger{})
	res, err := sparkwing.Bash(ctx, "pwd").Dir("sub").Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := strings.TrimSpace(res.Stdout)
	// On macOS, t.TempDir paths get a /private prefix when cd'd into
	// (symlink resolution). Compare on the suffix.
	if !strings.HasSuffix(got, filepath.Join(root, "sub")) && !strings.HasSuffix(got, "/sub") {
		t.Fatalf("pwd = %q, expected suffix %q", got, filepath.Join(root, "sub"))
	}
}

// An absolute dir is used as-is (not joined onto WorkDir()).
func TestCmd_AbsoluteDirPassesThrough(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	prev := sparkwing.WorkDir()
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })

	ctx := sparkwing.WithLogger(context.Background(), &recordingLogger{})
	res, err := sparkwing.Bash(ctx, "pwd").Dir(other).Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := strings.TrimSpace(res.Stdout)
	if !strings.HasSuffix(got, other) {
		t.Fatalf("pwd = %q, want suffix %q (absolute dir must not be rewritten under WorkDir)", got, other)
	}
}

func TestCmd_StringTrimsStdout(t *testing.T) {
	ctx := sparkwing.WithLogger(context.Background(), &recordingLogger{})
	out, err := sparkwing.Bash(ctx, "printf 'hello\\n\\n'").String()
	if err != nil {
		t.Fatalf("String: %v", err)
	}
	if out != "hello" {
		t.Fatalf("String() = %q, want %q", out, "hello")
	}
}

func TestCmd_LinesSplitsAndDropsBlanks(t *testing.T) {
	ctx := sparkwing.WithLogger(context.Background(), &recordingLogger{})
	lines, err := sparkwing.Bash(ctx, `printf 'a\n\nb\n  c  \n'`).Lines()
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	want := []string{"a", "b", "c"}
	if len(lines) != len(want) {
		t.Fatalf("Lines() = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("Lines()[%d] = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestCmd_JSONDecodes(t *testing.T) {
	ctx := sparkwing.WithLogger(context.Background(), &recordingLogger{})
	var got struct {
		Items []string `json:"items"`
	}
	err := sparkwing.Bash(ctx, `printf '{"items":["a","b"]}'`).JSON(&got)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if len(got.Items) != 2 || got.Items[0] != "a" || got.Items[1] != "b" {
		t.Fatalf("decoded = %+v", got)
	}
}

func TestCmd_JSONFailurePreservesExecError(t *testing.T) {
	ctx := sparkwing.WithLogger(context.Background(), &recordingLogger{})
	var dst map[string]any
	err := sparkwing.Bash(ctx, "exit 11").JSON(&dst)
	if err == nil {
		t.Fatal("expected error")
	}
	var ee *sparkwing.ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err is not *ExecError: %T", err)
	}
	if ee.ExitCode != 11 {
		t.Fatalf("ExitCode = %d, want 11", ee.ExitCode)
	}
}

func TestCmd_MustBeEmptyHappyPath(t *testing.T) {
	ctx := sparkwing.WithLogger(context.Background(), &recordingLogger{})
	if err := sparkwing.Bash(ctx, "true").MustBeEmpty("should be quiet"); err != nil {
		t.Fatalf("MustBeEmpty: %v", err)
	}
}

func TestCmd_MustBeEmptyFlagsOutput(t *testing.T) {
	ctx := sparkwing.WithLogger(context.Background(), &recordingLogger{})
	err := sparkwing.Bash(ctx, "echo offending-file.go").MustBeEmpty("formatting drift")
	if err == nil {
		t.Fatal("expected error for non-empty stdout")
	}
	if !strings.Contains(err.Error(), "formatting drift") {
		t.Fatalf("missing reason: %q", err)
	}
	if !strings.Contains(err.Error(), "offending-file.go") {
		t.Fatalf("missing offending stdout: %q", err)
	}
}
