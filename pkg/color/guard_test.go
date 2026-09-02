package color_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var allowed = map[string]bool{
	"pkg/color/color.go":                       true,
	"pkg/color/guard_test.go":                  true,
	"internal/logpretty/pretty.go":             true,
	"internal/logpretty/sanitize.go":           true,
	"internal/orchestrator/jobs_cli.go":        true,
	"internal/orchestrator/jobs_cli_remote.go": true,
}

var ansiPattern = regexp.MustCompile(`\\033\[|\\x1b\[`)

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
