// Package gotoolchain satisfies pipeline modules' Go floors without changing pipeline runtime environments.
package gotoolchain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/version"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"
)

// Report describes the effective Go toolchain and a pipeline module's floor.
type Report struct {
	Floor, Current, Setting, Source, Override, Module string
}

// Error explains a Go floor that the effective toolchain cannot satisfy.
type Error struct {
	Required, Current, Setting, Source, Module string
}

func (e *Error) Error() string {
	module := e.Module
	if module == "" {
		module = "a pipeline dependency"
	}
	fix := "run 'go env -u GOTOOLCHAIN'"
	if e.Source == envSource {
		fix = "unset GOTOOLCHAIN"
	}
	return fmt.Sprintf("%s requires go %s, but GOTOOLCHAIN=%s (set in %s) keeps Go at %s. Install Go %s or newer, or let Go download it: %s.", module, e.Required, e.Setting, e.Source, strings.TrimPrefix(e.Current, "go"), e.Required, fix)
}

const envSource = "the GOTOOLCHAIN environment variable"

type sessionKey struct{}

type session struct {
	env        []string
	notice     func(string)
	once       sync.Once
	report     Report
	err        error
	noticeOnce sync.Once
}

// WithSession shares one environment probe and one notice across a CLI invocation.
func WithSession(ctx context.Context, env []string, notice func(string)) context.Context {
	if _, ok := ctx.Value(sessionKey{}).(*session); ok {
		return ctx
	}
	if notice == nil {
		notice = func(message string) { fmt.Fprintln(os.Stderr, message) }
	}
	return context.WithValue(ctx, sessionKey{}, &session{env: effectiveEnv(env), notice: notice})
}

func getSession(ctx context.Context, env []string) *session {
	if s, ok := ctx.Value(sessionKey{}).(*session); ok {
		return s
	}
	return &session{env: effectiveEnv(env), notice: func(message string) { fmt.Fprintln(os.Stderr, message) }}
}

func effectiveEnv(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	return append([]string(nil), env...)
}

// Inspect reports whether the pipeline module can build under the effective toolchain.
func Inspect(ctx context.Context, dir string, env []string, overlay string) (Report, error) {
	return inspect(ctx, getSession(ctx, env), dir, overlay)
}

func inspect(ctx context.Context, s *session, dir, overlay string) (Report, error) {
	floor, module, err := moduleFloor(dir, overlay)
	if err != nil || floor == "" {
		return Report{}, err
	}
	s.once.Do(func() { s.report, s.err = probe(ctx, s.env) })
	r := s.report
	r.Floor, r.Module = floor, module
	if s.err != nil {
		return r, s.err
	}
	if version.Compare(r.Current, toolchain(floor)) >= 0 || switches(r.Setting) {
		return r, nil
	}
	if r.Setting == "local" {
		return r, &Error{Required: floor, Current: r.Current, Setting: r.Setting, Source: r.Source, Module: module}
	}
	if version.IsValid(r.Setting) && version.Compare(r.Setting, toolchain(floor)) < 0 {
		r.Override = toolchain(floor)
	}
	return r, nil
}

// BuildEnv returns a separate build-only environment, for go commands run in dir, satisfying the module's floor.
func BuildEnv(ctx context.Context, dir string, env []string, overlay string) ([]string, error) {
	s := getSession(ctx, env)
	r, err := inspect(ctx, s, dir, overlay)
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil && !errors.Is(err, s.err) {
		return nil, err
	}
	if err != nil {
		// A failed probe only loses the override; go itself still reports a real floor mismatch.
		slog.Default().Debug("go toolchain probe failed", "err", err)
		r = Report{}
	}
	result := effectiveEnv(env)
	if env == nil {
		result = append([]string(nil), s.env...)
	}
	// os/exec only sets PWD for a command's Dir when its Env is nil, and this env is always explicit.
	if abs, err := filepath.Abs(dir); err == nil {
		result = setEnv(result, "PWD", abs)
	}
	if r.Override != "" {
		result = setEnv(result, "GOTOOLCHAIN", r.Override)
		if s.notice != nil {
			s.noticeOnce.Do(func() {
				s.notice(fmt.Sprintf("sparkwing: building .sparkwing/ with %s (its go.mod requires go %s; GOTOOLCHAIN pins %s)", r.Override, r.Floor, r.Setting))
			})
		}
	}
	return result, nil
}

func moduleFloor(dir, overlay string) (string, string, error) {
	paths := []string{filepath.Join(dir, "go.mod")}
	if overlay != "" {
		if !filepath.IsAbs(overlay) {
			overlay = filepath.Join(dir, overlay)
		}
		paths = append(paths, overlay)
	}
	var floor, origin string
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		file, err := modfile.ParseLax(path, data, nil)
		if err != nil {
			return "", "", err
		}
		if file.Go != nil && (floor == "" || version.Compare("go"+file.Go.Version, "go"+floor) > 0) {
			floor, origin = file.Go.Version, path
		}
	}
	return floor, origin, nil
}

func toolchain(floor string) string {
	if strings.Count(floor, ".") == 1 {
		floor += ".0"
	}
	return "go" + floor
}

func switches(setting string) bool {
	return setting == "auto" || setting == "path" || strings.HasSuffix(setting, "+auto") || strings.HasSuffix(setting, "+path")
}

func probe(ctx context.Context, env []string) (Report, error) {
	dir, err := os.MkdirTemp("", "sparkwing-go-env-")
	if err != nil {
		return Report{}, err
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(os.Stderr, "sparkwing: remove Go probe directory: %v\n", err)
		}
	}()
	path, err := goPath(env)
	if err != nil {
		return Report{}, err
	}
	cmd := exec.Command(path, "env", "GOTOOLCHAIN", "GOVERSION", "GOENV")
	cmd.Dir, cmd.Env = dir, setEnv(env, "GOWORK", "off")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := Run(ctx, cmd); err != nil {
		return Report{}, fmt.Errorf("probe Go toolchain: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	out := stdout.Bytes()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 || !version.IsValid(strings.TrimSpace(lines[1])) {
		return Report{}, fmt.Errorf("probe Go toolchain: unexpected go env output %q", out)
	}
	r := Report{Setting: strings.TrimSpace(lines[0]), Current: strings.TrimSpace(lines[1]), Source: "the Go toolchain default"}
	if value, ok := envValue(env, "GOTOOLCHAIN"); ok && value != "" {
		r.Source = envSource
	} else if lines[2] != "off" {
		data, readErr := os.ReadFile(strings.TrimSpace(lines[2]))
		if readErr != nil && !os.IsNotExist(readErr) {
			return Report{}, fmt.Errorf("read Go environment file: %w", readErr)
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			if value, ok := strings.CutPrefix(line, "GOTOOLCHAIN="); ok && value != "" {
				r.Source = strings.TrimSpace(lines[2])
			}
		}
	}
	return r, nil
}

func goPath(env []string) (string, error) {
	path, _ := envValue(env, "PATH")
	for _, dir := range filepath.SplitList(path) {
		name := "go"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode()&0o111 != 0) {
			return filepath.Abs(candidate)
		}
	}
	return "", fmt.Errorf("probe Go toolchain: go executable not found in PATH")
}

func envValue(env []string, key string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if value, ok := strings.CutPrefix(env[i], key+"="); ok {
			return value, true
		}
	}
	return "", false
}

func setEnv(env []string, key, value string) []string {
	result := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, key+"=") {
			result = append(result, item)
		}
	}
	return append(result, key+"="+value)
}

var mismatch = regexp.MustCompile(`requires go >= ([^\s]+) \(running go ([^;\s]+); GOTOOLCHAIN=([^\)\s]+)\)`)

// ExplainOutput replaces Go's toolchain-floor failure with an actionable typed error.
func ExplainOutput(ctx context.Context, output string, env []string) error {
	match := mismatch.FindStringSubmatch(output)
	if match == nil {
		return nil
	}
	s := getSession(ctx, env)
	s.once.Do(func() { s.report, s.err = probe(ctx, s.env) })
	source := s.report.Source
	if source == "" {
		source = "the Go toolchain default"
		if value, ok := envValue(effectiveEnv(env), "GOTOOLCHAIN"); ok && value != "" {
			source = envSource
		}
	}
	return &Error{Required: match[1], Current: "go" + match[2], Setting: match[3], Source: source}
}
