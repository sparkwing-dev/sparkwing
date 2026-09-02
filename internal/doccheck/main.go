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
	line     int
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
	ok = checkProfileConfigs(contentDir) && ok
	fmt.Println()
	ok = checkBannedTokens(contentDir, repoRoot) && ok
	fmt.Println()
	ok = checkFailureReasons(contentDir, repoRoot) && ok
	fmt.Println()
	ok = checkFrozenCounts(contentDir) && ok
	fmt.Println()
	ok = checkLinks(contentDir) && ok
	fmt.Println()
	ok = checkCLIVerbs(contentDir, repoRoot) && ok
	fmt.Println()
	ok = checkServicePorts(contentDir, repoRoot) && ok
	fmt.Println()
	ok = checkAuxDocs(repoRoot) && ok
	fmt.Println()
	ok = checkSidebar(contentDir) && ok
	if !ok {
		os.Exit(1)
	}
}

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
		// #nosec G703 -- a build-time tool reading paths the operator names
		if err := os.WriteFile(filepath.Join(dir, "doc.go"), []byte(src), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(2)
		}
		cmd := exec.Command("go", "build", "-o", os.DevNull, "./b"+fmt.Sprintf("%03d", i))
		cmd.Dir = tmp
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
		out, berr := cmd.CombinedOutput()
		if berr == nil {
			continue
		}
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
	// #nosec G703 -- a build-time tool reading paths the operator names
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		if strings.Contains(path, "/migrations/") || strings.Contains(path, "/proposals/") {
			return nil
		}
		// #nosec G122 -- a TOCTOU swap here needs write access to the checkout this build-time check already trusts
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
	gomod, err := moduleGoMod(repoRoot)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte(gomod), 0o644); err != nil {
		return err
	}
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

func moduleGoMod(repoRoot string) (string, error) {
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolve repo root %q: %w", repoRoot, err)
	}
	return fmt.Sprintf("module doccheck\n\ngo 1.26\n\nrequire github.com/sparkwing-dev/sparkwing v0.0.0\n\nreplace github.com/sparkwing-dev/sparkwing => %s\n", abs), nil
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
