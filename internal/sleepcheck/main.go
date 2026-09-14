package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/gitenv"
)

const baselineRelPath = "internal/sleepcheck/baseline.txt"

var skipDirs = map[string]bool{
	"vendor":          true,
	"testdata":        true,
	"node_modules":    true,
	".git":            true,
	".claude-scratch": true,
}

var bannedCalls = map[string]bool{
	"Sleep":     true,
	"After":     true,
	"Tick":      true,
	"NewTimer":  true,
	"NewTicker": true,
}

type finding struct {
	file string
	line int
	form string
}

func (f finding) key() string {
	return fmt.Sprintf("%s:%d", f.file, f.line)
}

const usageText = `usage: sleepcheck [-staged | -base ref] [-allow-no-diff] [-write-baseline] [-baseline path] <root>

<root> is one directory to walk, normally the repository root; sleepcheck
takes no list of files. It parses every _test.go file under <root> and skips
vendor, testdata, node_modules, .git and .claude-scratch.

Banned in a test: time.Sleep, time.After, time.Tick, time.NewTimer,
time.NewTicker, and time.Now or time.Since read as a wait or a deadline (a
greater-or-less comparison, a loop condition, or a context.WithTimeout or
WithDeadline argument). time.Now() that only stamps a fixture value is allowed.

-staged and -base narrow the report to the lines those diffs add; -base also
reads untracked _test.go files, which git diff alone leaves out. A run with
neither judges the whole tree against the baseline file, fails any finding the
baseline does not carry, and names each baseline entry that no longer offends
so the file only shrinks. -write-baseline rewrites that file from today's
offenders.

flags:
`

const advice = `A test that waits on the wall clock passes on an idle box and fails on a loaded
one. Instead of sleeping or reading the clock:
  - wait on the channel, condition, or state the code under test signals
  - inject a clock the test controls, or a fake the code under test accepts
  - use testing/synctest, whose clock advances once every goroutine is blocked
time.Now() that only stamps a fixture value stays allowed.
Fix the test rather than adding a line to ` + baselineRelPath + `; that file
lists the offenders that predate this rule and only ever shrinks.
`

const alternatives = "wait on a signaled condition, inject a fake clock, or use testing/synctest"

func usage() {
	fmt.Fprint(flag.CommandLine.Output(), usageText)
	flag.PrintDefaults()
}

func main() {
	staged := flag.Bool("staged", false, "only report waits in the staged diff")
	base := flag.String("base", "", "only report waits added vs the fork point from this git ref")
	allowNoDiff := flag.Bool("allow-no-diff", false, "pass instead of failing when the diff cannot be computed; the run gates nothing")
	write := flag.Bool("write-baseline", false, "rewrite the baseline file from the offenders in the tree")
	baselinePath := flag.String("baseline", "", "baseline file to read or write (default "+baselineRelPath+" under <root>)")
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() != 1 {
		usage()
		os.Exit(2)
	}
	root := flag.Arg(0)

	findings, err := scan(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sleepcheck:", err)
		os.Exit(2)
	}
	path := *baselinePath
	if path == "" {
		path = filepath.Join(root, filepath.FromSlash(baselineRelPath))
	}

	if *write {
		if *staged || *base != "" {
			fmt.Fprintln(os.Stderr, "sleepcheck: -write-baseline records the whole tree, so it takes neither -staged nor -base")
			os.Exit(2)
		}
		if err := writeBaseline(path, findings); err != nil {
			fmt.Fprintln(os.Stderr, "sleepcheck:", err)
			os.Exit(2)
		}
		fmt.Printf("sleepcheck: wrote %d offender(s) to %s\n", len(findings), path)
		return
	}

	if *staged || *base != "" {
		added, aerr := scopedAdds(root, *staged, *base)
		if aerr != nil {
			if !*allowNoDiff {
				fmt.Fprintln(os.Stderr, diffFailure(*base, aerr))
				os.Exit(2)
			}
			fmt.Fprintf(os.Stderr, "sleepcheck: cannot compute the diff (%v); -allow-no-diff accepted a run that gates nothing\n", aerr)
			fmt.Println("sleepcheck: skipped (no diff)")
			return
		}
		findings = onlyAdded(findings, added)
		if len(findings) > 0 {
			report(findings, "in the change this run judged")
			os.Exit(1)
		}
		fmt.Println("sleepcheck: clean")
		return
	}

	baseline, berr := readBaseline(path)
	if berr != nil {
		fmt.Fprintln(os.Stderr, "sleepcheck:", berr)
		os.Exit(2)
	}
	fresh, stale := split(findings, baseline)
	if len(stale) > 0 {
		fmt.Print(staleReport(stale, path))
	}
	if len(fresh) > 0 {
		report(fresh, "outside the baseline")
		os.Exit(1)
	}
	fmt.Printf("sleepcheck: clean (%d baselined offender(s))\n", len(findings))
}

func diffFailure(base string, err error) string {
	scope := "the staged diff"
	if base != "" {
		scope = base
	}
	return fmt.Sprintf("sleepcheck: cannot compute the diff against %s (%v), so nothing was gated.\n"+
		"Fix: fetch the base ref and name it, for example `git fetch origin main` then "+
		"`sleepcheck -base origin/main <root>`.\n"+
		"Pass -allow-no-diff to accept a run that gates nothing.", scope, err)
}

func scan(root string) ([]finding, error) {
	var findings []finding
	var unread []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		f, ferr := checkFile(path, filepath.ToSlash(rel))
		if ferr != nil {
			unread = append(unread, ferr.Error())
			return nil
		}
		findings = append(findings, f...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// safety: a test file the parser rejected carries no verdict, and a run that
	// passes it reports health it never established.
	if len(unread) > 0 {
		sort.Strings(unread)
		return nil, fmt.Errorf("%d test file(s) could not be parsed, so this run reached no verdict on them:\n%s",
			len(unread), strings.Join(unread, "\n"))
	}
	sortFindings(findings)
	return findings, nil
}

func checkFile(path, rel string) ([]finding, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	timePkg := importAlias(file, "time")
	if timePkg == "" {
		return nil, nil
	}
	ctxPkg := importAlias(file, "context")
	elapsed := elapsedNames(file, timePkg)

	var out []finding
	seen := map[int]bool{}
	add := func(pos token.Pos, form string) {
		line := fset.Position(pos).Line
		if seen[line] {
			return
		}
		seen[line] = true
		out = append(out, finding{file: rel, line: line, form: form})
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.ForStmt:
			if node.Cond != nil && readsClock(node.Cond, timePkg, elapsed) {
				add(node.Cond.Pos(), "a loop that waits on the wall clock")
			}
		case *ast.BinaryExpr:
			if isOrdering(node.Op) && readsClock(node, timePkg, elapsed) {
				add(node.Pos(), "a deadline compared against the wall clock")
			}
		case *ast.CallExpr:
			if name, ok := pkgCall(node.Fun, timePkg); ok && bannedCalls[name] {
				add(node.Pos(), "time."+name)
				return true
			}
			if ctxPkg == "" {
				return true
			}
			name, ok := pkgCall(node.Fun, ctxPkg)
			if !ok || (name != "WithTimeout" && name != "WithDeadline") {
				return true
			}
			for _, arg := range node.Args {
				if readsClock(arg, timePkg, elapsed) {
					add(node.Pos(), "context."+name+" on a wall-clock deadline")
					break
				}
			}
		}
		return true
	})
	return out, nil
}

func importAlias(file *ast.File, path string) string {
	for _, spec := range file.Imports {
		value, err := strconv.Unquote(spec.Path.Value)
		if err != nil || value != path {
			continue
		}
		if spec.Name == nil {
			return filepath.Base(path)
		}
		if spec.Name.Name == "_" || spec.Name.Name == "." {
			return ""
		}
		return spec.Name.Name
	}
	return ""
}

// safety: a rule reading only the comparison misses `elapsed :=
// time.Since(start)` followed by `if elapsed > budget`. A time.Now() stamp is
// untracked: comparing one asserts about a value rather than waiting.
func elapsedNames(file *ast.File, timePkg string) map[string]bool {
	names := map[string]bool{}
	record := func(lhs, rhs []ast.Expr) {
		if len(lhs) != len(rhs) {
			return
		}
		for i, expr := range rhs {
			if !callsClock(expr, timePkg, "Since") {
				continue
			}
			if ident, ok := lhs[i].(*ast.Ident); ok {
				names[ident.Name] = true
			}
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			record(node.Lhs, node.Rhs)
		case *ast.ValueSpec:
			lhs := make([]ast.Expr, 0, len(node.Names))
			for _, name := range node.Names {
				lhs = append(lhs, name)
			}
			record(lhs, node.Values)
		}
		return true
	})
	return names
}

func readsClock(expr ast.Expr, timePkg string, elapsed map[string]bool) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.CallExpr:
			if name, ok := pkgCall(node.Fun, timePkg); ok && (name == "Now" || name == "Since") {
				found = true
			}
		case *ast.Ident:
			if elapsed[node.Name] {
				found = true
			}
		}
		return !found
	})
	return found
}

func callsClock(expr ast.Expr, timePkg, fn string) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return !found
		}
		if name, ok := pkgCall(call.Fun, timePkg); ok && name == fn {
			found = true
		}
		return !found
	})
	return found
}

func pkgCall(fun ast.Expr, pkg string) (string, bool) {
	if pkg == "" {
		return "", false
	}
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok || ident.Name != pkg {
		return "", false
	}
	return sel.Sel.Name, true
}

func isOrdering(op token.Token) bool {
	return op == token.LSS || op == token.GTR || op == token.LEQ || op == token.GEQ
}

func readBaseline(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	entries := map[string]bool{}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries[line] = true
	}
	return entries, nil
}

func writeBaseline(path string, findings []finding) error {
	var b strings.Builder
	b.WriteString(baselineHeader)
	keys := make([]string, 0, len(findings))
	for _, f := range findings {
		keys = append(keys, f.key())
	}
	sort.Strings(keys)
	for _, key := range keys {
		b.WriteString(key)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

const baselineHeader = `# Tests that sleep or wait on the wall clock, one path:line each, recorded when
# the rule landed. sleepcheck fails any finding this file does not carry, and
# names an entry that no longer offends so the list only shrinks.
# Regenerate after a purge: GOWORK=off go run ./internal/sleepcheck -write-baseline .
`

func split(findings []finding, baseline map[string]bool) (fresh []finding, stale []string) {
	live := map[string]bool{}
	for _, f := range findings {
		key := f.key()
		live[key] = true
		if !baseline[key] {
			fresh = append(fresh, f)
		}
	}
	for key := range baseline {
		if !live[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	return fresh, stale
}

func staleReport(stale []string, path string) string {
	var b strings.Builder
	for _, key := range stale {
		fmt.Fprintf(&b, "%s: baselined wait is gone\n", key)
	}
	fmt.Fprintf(&b, "\nsleepcheck: %d stale baseline entry(ies) in %s. The list only shrinks: "+
		"run `go run ./internal/sleepcheck -write-baseline .` to drop them.\n\n", len(stale), path)
	return b.String()
}

func report(findings []finding, scope string) {
	for _, f := range findings {
		fmt.Printf("%s:%d: %s. Instead: %s.\n", f.file, f.line, f.form, alternatives)
	}
	fmt.Printf("\nsleepcheck: %d test(s) wait on the wall clock %s.\n\n", len(findings), scope)
	fmt.Print(advice)
}

func sortFindings(findings []finding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].file != findings[j].file {
			return findings[i].file < findings[j].file
		}
		return findings[i].line < findings[j].line
	})
}

func onlyAdded(findings []finding, added map[string]map[int]bool) []finding {
	var out []finding
	for _, f := range findings {
		if added[f.file][f.line] {
			out = append(out, f)
		}
	}
	return out
}

func scopedAdds(root string, staged bool, base string) (map[string]map[int]bool, error) {
	// safety: git quotes a path holding a non-ASCII byte unless core.quotePath
	// is off, and a quoted +++ header drops that file from the scope unjudged.
	args := []string{"-c", "core.quotePath=false", "diff", "--unified=0", "--no-color"}
	var index string
	if staged {
		index = stagedIndex()
		args = append(args, "--cached")
	} else {
		forkPoint := base
		if out, err := git(root, "", "merge-base", base, "HEAD"); err == nil {
			forkPoint = strings.TrimSpace(out)
		}
		args = append(args, forkPoint)
	}
	args = append(args, "--", "*_test.go")

	diff, err := git(root, index, args...)
	if err != nil {
		return nil, err
	}
	added := parseAddedLines(diff)
	if !staged {
		if err := addUntracked(root, added); err != nil {
			return nil, err
		}
	}
	return added, nil
}

func addUntracked(root string, added map[string]map[int]bool) error {
	out, err := git(root, "", "ls-files", "--others", "--exclude-standard", "-z", "--", "*_test.go")
	if err != nil {
		return err
	}
	for rel := range strings.SplitSeq(out, "\x00") {
		if rel == "" {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(root, rel))
		if rerr != nil {
			if os.IsNotExist(rerr) {
				continue
			}
			return rerr
		}
		set := added[rel]
		if set == nil {
			set = map[int]bool{}
			added[rel] = set
		}
		for line := 1; line <= strings.Count(string(data), "\n")+1; line++ {
			set[line] = true
		}
	}
	return nil
}

func stagedIndex() string {
	if os.Getenv("GIT_INDEX_FILE") != "" {
		return ""
	}
	return gitenv.GateIndex()
}

var hunkRE = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

func parseAddedLines(diff string) map[string]map[int]bool {
	added := map[string]map[int]bool{}
	var cur string
	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			cur = strings.TrimPrefix(line, "+++ b/")
		case strings.HasPrefix(line, "+++ "):
			cur = ""
		case strings.HasPrefix(line, "@@") && cur != "":
			m := hunkRE.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			start, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			count := 1
			if m[2] != "" {
				parsed, perr := strconv.Atoi(m[2])
				if perr != nil {
					continue
				}
				count = parsed
			}
			set := added[cur]
			if set == nil {
				set = map[int]bool{}
				added[cur] = set
			}
			for i := 0; i < count; i++ {
				set[start+i] = true
			}
		}
	}
	return added
}

func git(root, index string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if index != "" {
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
	}
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}
