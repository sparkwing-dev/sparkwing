package color_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// allowed is the set of files permitted to contain raw ANSI escape
// sequences. Each entry is a path relative to the module root.
//
//   - pkg/color/color.go: the sanctioned helper itself.
//   - internal/logpretty/pretty.go: PrettyRenderer's internal palette
//     (extracted from internal/orchestrator/logger.go). The renderer
//     is only selected when stdout is a TTY (see orchestrator's
//     selectLocalRenderer), so its raw codes never reach an agent.
//   - orchestrator/jobs_cli.go,
//     orchestrator/jobs_cli_remote.go: cursor-control escapes
//     (\x1b[H, \x1b[J) for live-status redraws; only run in
//     interactive mode.
var allowed = map[string]bool{
	"pkg/color/color.go":                       true,
	"pkg/color/guard_test.go":                  true,
	"internal/logpretty/pretty.go":             true,
	"internal/orchestrator/jobs_cli.go":        true,
	"internal/orchestrator/jobs_cli_remote.go": true,
}

// ansiPattern matches both common Go-source representations of an
// ANSI CSI introducer: `\033[` (octal escape) and `\x1b[` (hex
// escape). Doesn't match unicode escape `[` because no
// existing source uses that form; if a future caller does, add it.
var ansiPattern = regexp.MustCompile(`\\033\[|\\x1b\[`)

// TestNoRawANSIOutsideAllowed walks the module tree and fails on any
// non-test, non-vendored Go source that contains a raw ANSI escape
// outside the allowed list. Agents see stdout as a pipe, so raw
// ANSI bypasses pkg/color's TTY auto-disable and ends up as literal
// noise in their logs.
func TestNoRawANSIOutsideAllowed(t *testing.T) {
	root := moduleRoot(t)
	var offenders []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			if name == "node_modules" || name == "vendor" || name == ".git" ||
				name == ".claude" || name == ".sparkwing" || name == "out" ||
				strings.HasPrefix(name, ".") && name != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if allowed[rel] {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if ansiPattern.Match(body) {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(offenders) > 0 {
		t.Fatalf("raw ANSI escape sequences found outside allowed files:\n  %s\n\n"+
			"Use pkg/color helpers (color.Green, color.Red, color.Bold, ...) so\n"+
			"output stays clean for agents and pipes. If your use is genuinely\n"+
			"outside the color system (cursor control, etc.) and is gated on a\n"+
			"TTY, add the file to `allowed` in pkg/color/guard_test.go with a\n"+
			"comment explaining why.",
			strings.Join(offenders, "\n  "))
	}
}

// moduleRoot walks up from the test's CWD until it finds go.mod,
// returning that directory. The test's working dir is the package's
// own directory at run time, so we step up once or twice to land on
// the module root.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for range 6 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not locate go.mod walking up from test cwd")
	return ""
}
