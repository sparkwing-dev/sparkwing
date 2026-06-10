// doccheck extracts ```go fenced blocks from the sparkwing docs and
// compiles each against the in-repo SDK, so a doc example that references
// a removed symbol or a wrong signature fails loudly instead of misleading
// a reader (or an agent).
//
// Blocks that are deliberately illustrative and not meant to compile must
// carry an HTML comment immediately above the fence:
//
//	<!-- doccheck:skip reason text -->
//	```go
//	... non-compiling snippet ...
//	```
//
// Skips are reported (count + reasons), never silent.
//
// Usage: go run . <docs-content-dir> <sparkwing-repo-root>
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type block struct {
	file     string
	line     int // 1-based line of the first code line
	body     string
	topLevel bool
	skip     string
}

var (
	skipRE    = regexp.MustCompile(`<!--\s*doccheck:skip\s+(.*?)\s*-->`)
	topDeclRE = regexp.MustCompile(`^(func|type|const|var|import)\b`)
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: doccheck <docs-content-dir> <repo-root>")
		os.Exit(2)
	}
	contentDir, repoRoot := os.Args[1], os.Args[2]

	ok := checkGoBlocks(contentDir, repoRoot)
	fmt.Println()
	ok = checkYAMLConfigs(contentDir) && ok
	fmt.Println()
	ok = checkBannedTokens(contentDir, repoRoot) && ok
	fmt.Println()
	ok = checkFailureReasons(contentDir, repoRoot) && ok
	fmt.Println()
	ok = checkFrozenCounts(contentDir) && ok
	if !ok {
		os.Exit(1)
	}
}

// checkGoBlocks compiles every ```go fenced block under contentDir
// against the in-repo SDK and reports SDK-API drift. Returns false on
// any drift.
func checkGoBlocks(contentDir, repoRoot string) bool {
	blocks, err := extract(contentDir, "go")
	if err != nil {
		fmt.Fprintln(os.Stderr, "extract:", err)
		os.Exit(2)
	}

	tmp, err := os.MkdirTemp("", "doccheck-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mktemp:", err)
		os.Exit(2)
	}
	defer os.RemoveAll(tmp)
	if err := writeModule(tmp, repoRoot); err != nil {
		fmt.Fprintln(os.Stderr, "module setup:", err)
		os.Exit(2)
	}

	var failed, skipped, checked, partial int
	var failures []string
	skipReasons := map[string]int{}

	for i, b := range blocks {
		if b.skip != "" {
			skipped++
			skipReasons[b.skip]++
			continue
		}
		checked++
		src := harness(b)
		dir := filepath.Join(tmp, fmt.Sprintf("b%03d", i))
		_ = os.MkdirAll(dir, 0o755)
		if err := os.WriteFile(filepath.Join(dir, "doc.go"), []byte(src), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(2)
		}
		cmd := exec.Command("go", "build", "-o", os.DevNull, "./b"+fmt.Sprintf("%03d", i))
		cmd.Dir = tmp
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
		out, berr := cmd.CombinedOutput()
		if berr == nil {
			continue // compiled clean
		}
		// Only SDK-API drift (wrong/removed sparkwing|sw symbol or
		// signature) is a real doc bug. Errors about undeclared business
		// identifiers / unused vars are snippet-not-self-contained noise,
		// not something the doc author got wrong about the API.
		drift := sdkDriftLines(string(out))
		if len(drift) == 0 {
			partial++
			continue
		}
		failed++
		failures = append(failures, fmt.Sprintf("%s:%d\n%s", b.file, b.line, indent(strings.Join(drift, "\n"))))
	}

	clean := checked - failed - partial
	fmt.Printf("doccheck: %d blocks -- %d skipped, %d checked (%d SDK-clean, %d partial/non-self-contained, %d SDK-DRIFT)\n",
		len(blocks), skipped, checked, clean, partial, failed)
	if len(skipReasons) > 0 {
		fmt.Println("\nskipped (by reason):")
		var rs []string
		for r := range skipReasons {
			rs = append(rs, r)
		}
		sort.Strings(rs)
		for _, r := range rs {
			fmt.Printf("  %2d  %s\n", skipReasons[r], r)
		}
	}
	if failed > 0 {
		fmt.Printf("\n%d block(s) reference a wrong/removed SDK symbol or signature:\n\n", failed)
		for _, f := range failures {
			fmt.Println(f)
		}
		return false
	}
	fmt.Println("\nNO SDK-API DRIFT IN DOC EXAMPLES")
	return true
}

var (
	sdkRefRE     = regexp.MustCompile(`\b(sparkwing|sw)\.[A-Z]\w*`)
	sdkProblemRE = regexp.MustCompile(`undefined|has no field or method|unknown field|arguments in call|cannot use|mismatched types|not enough arguments|too many`)
)

// sdkDriftLines keeps only compiler error lines that indicate the doc
// misused the sparkwing/sw API (referenced a removed symbol, a method
// that doesn't exist on *Plan/*Work, or a wrong signature). Everything
// else (undeclared business idents, unused vars) is harness noise from
// a snippet that isn't self-contained.
func sdkDriftLines(out string) []string {
	var keep []string
	for _, l := range strings.Split(out, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if sdkRefRE.MatchString(t) && sdkProblemRE.MatchString(t) {
			keep = append(keep, t)
		}
	}
	return keep
}

func extract(dir, lang string) ([]block, error) {
	fenceOpen := regexp.MustCompile("^```" + lang + `(\s|$)`)
	var out []block
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		// migrations/ and proposals/ intentionally show old or future
		// APIs (design history) -- the docs sidebar already excludes them,
		// so they're not part of the current-API gate.
		if strings.Contains(path, "/migrations/") || strings.Contains(path, "/proposals/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(data), "\n")
		rel, _ := filepath.Rel(dir, path)
		for i := 0; i < len(lines); i++ {
			if !fenceOpen.MatchString(lines[i]) {
				continue
			}
			skip := ""
			// look back over blank lines for a skip marker
			for k := i - 1; k >= 0; k-- {
				t := strings.TrimSpace(lines[k])
				if t == "" {
					continue
				}
				if m := skipRE.FindStringSubmatch(t); m != nil {
					skip = m[1]
				}
				break
			}
			start := i + 1
			j := start
			for j < len(lines) && !strings.HasPrefix(lines[j], "```") {
				j++
			}
			body := strings.Join(lines[start:j], "\n")
			out = append(out, block{
				file: rel, line: start + 1, body: body,
				topLevel: hasTopDecl(lines[start:j]), skip: skip,
			})
			i = j
		}
		return nil
	})
	return out, err
}

func hasTopDecl(lines []string) bool {
	for _, l := range lines {
		if topDeclRE.MatchString(l) {
			return true
		}
	}
	return false
}

// harness wraps a block into a compilable Go file. Top-level decls go at
// package scope; statement fragments go inside a func with the common doc
// locals (ctx/plan/w/rc) predeclared. All common imports are force-used so
// an unused import never trips the build.
func harness(b block) string {
	const preamble = `package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
	sparkwing "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type _swKeep = sw.Plan
type _sparkwingKeep = sparkwing.Plan

var (
	_ = context.TODO
	_ = errors.New
	_ = fmt.Sprint
	_ = time.Second
)
`
	if b.topLevel {
		return preamble + "\n" + b.body + "\n"
	}
	return preamble + "\nfunc _docFragment(ctx context.Context, plan *sw.Plan, w *sw.Work, rc sw.RunContext) error {\n\t_, _, _, _ = ctx, plan, w, rc\n" + b.body + "\n\treturn nil\n}\n"
}

func writeModule(tmp, repoRoot string) error {
	gomod := fmt.Sprintf("module doccheck\n\ngo 1.26\n\nrequire github.com/sparkwing-dev/sparkwing v0.0.0\n\nreplace github.com/sparkwing-dev/sparkwing => %s\n", repoRoot)
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte(gomod), 0o644); err != nil {
		return err
	}
	// A trivial root file so `go mod tidy` has a package to anchor on.
	root := "package doccheck\n\nimport _ \"github.com/sparkwing-dev/sparkwing/sparkwing\"\n"
	if err := os.WriteFile(filepath.Join(tmp, "root.go"), []byte(root), 0o644); err != nil {
		return err
	}
	cmd := exec.Command("go", "mod", "tidy")
	cmd.Dir = tmp
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go mod tidy: %v\n%s", err, out)
	}
	return nil
}

func indent(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(s, "\n") {
		b.WriteString("    ")
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String()
}
