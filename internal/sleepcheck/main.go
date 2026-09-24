package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/gatescope"
)

var bannedCalls = map[string]bool{
	"Sleep":     true,
	"After":     true,
	"Tick":      true,
	"NewTimer":  true,
	"NewTicker": true,
}

var clockReads = map[string]bool{
	"Now":   true,
	"Since": true,
	"Until": true,
}

type finding struct {
	file   string
	line   int
	column int
	form   string
}

type unreadable struct {
	file string
	err  error
}

const unjudgeable = "cannot judge this file; import time by name"

const usageText = `usage: sleepcheck [-staged | -base ref] [-allow-no-diff] <root>

<root> is one directory to walk, normally the repository root; sleepcheck
takes no list of files. It parses every _test.go file under <root> and skips
vendor, testdata, node_modules, .git and .claude-scratch.

Banned in a test: time.Sleep, time.After, time.Tick, time.NewTimer and
time.NewTicker, whether called or taken as a value, and time.Now, time.Since
or time.Until read as a wait or a deadline (a greater-or-less comparison, a
loop condition, or a context.WithTimeout or WithDeadline argument). time.Now()
that only stamps a fixture value is allowed. A file that dot-imports time
cannot be judged and fails for that reason.

Four approved real HTTP/process bounds require an exact source marker,
file, function and duration; a stale or changed exception fails the run.

-staged and -base narrow the report to the lines those diffs add, which is how
a test written before this rule keeps passing; -base also reads untracked
_test.go files, which git diff alone leaves out. A run with neither judges
every test file in the tree. A _test.go file the parser rejects fails the run
when the scope names it, because a file the gate could not read is a file it
did not judge.

flags:
`

const advice = `A test that waits on the wall clock passes on an idle box and fails on a loaded
one. Instead of sleeping or reading the clock:
  - wait on the channel, condition, or state the code under test signals
  - inject a clock the test controls, or a fake the code under test accepts
  - use testing/synctest, whose clock advances once every goroutine is blocked
time.Now() that only stamps a fixture value stays allowed.
`

const alternatives = "wait on a signaled condition, inject a fake clock, or use testing/synctest"

func usage() {
	fmt.Fprint(flag.CommandLine.Output(), usageText)
	flag.PrintDefaults()
}

func main() {
	staged := flag.Bool("staged", false, "only report waits in the staged diff (the pre-commit gate)")
	base := flag.String("base", "", "only report waits added vs the fork point from this git ref")
	allowNoDiff := flag.Bool("allow-no-diff", false, "pass instead of failing when the diff cannot be computed; the run gates nothing")
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() != 1 {
		usage()
		os.Exit(2)
	}
	root := flag.Arg(0)

	findings, unread, err := scan(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sleepcheck:", err)
		os.Exit(2)
	}
	allowed, stale := approvedWaits(root, approvedExternalBoundaries)
	findings = withoutApproved(findings, allowed)
	unusedMarkers, err := unconsumedBoundaryMarkers(root, allowed)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sleepcheck:", err)
		os.Exit(2)
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
		unread = onlyChanged(unread, root, added)
	}

	if len(findings) > 0 {
		report(findings)
	}
	if len(unread) > 0 {
		fmt.Print(unreadableFailure(unread))
	}
	for _, rule := range stale {
		fmt.Printf("%s: approved external-boundary wait missing or changed in %s (%s)\n",
			rule.file, rule.function, rule.duration)
	}
	for _, marker := range unusedMarkers {
		fmt.Printf("%s:%d: %s\n", marker.file, marker.line, marker.form)
	}
	if len(findings) > 0 || len(unread) > 0 || len(stale) > 0 || len(unusedMarkers) > 0 {
		os.Exit(1)
	}
	fmt.Println("sleepcheck: clean")
}

func diffFailure(base string, err error) string {
	return gatescope.DiffFailure("sleepcheck", base, err)
}

func scan(root string) ([]finding, []unreadable, error) {
	var findings []finding
	var unread []unreadable
	err := gatescope.Walk(root, "_test.go", func(path, rel string) {
		f, ferr := checkFile(path, rel)
		if ferr != nil {
			unread = append(unread, unreadable{file: path, err: ferr})
			return
		}
		findings = append(findings, f...)
	})
	if err != nil {
		return nil, nil, err
	}
	sortFindings(findings)
	return findings, unread, nil
}

func checkFile(path, rel string) ([]finding, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	timePkg, dot := importAlias(file, "time")
	if dot {
		return []finding{{file: rel, line: fset.Position(file.Pos()).Line, form: unjudgeable}}, nil
	}
	if timePkg == "" {
		return nil, nil
	}
	ctxPkg, _ := importAlias(file, "context")
	elapsed := elapsedNames(file, timePkg)

	var out []finding
	seenClock := map[int]bool{}
	seenCalls := map[token.Pos]bool{}
	add := func(pos token.Pos, form string) {
		location := fset.Position(pos)
		if strings.HasPrefix(form, "time.") {
			if seenCalls[pos] {
				return
			}
			seenCalls[pos] = true
		} else {
			if seenClock[location.Line] {
				return
			}
			seenClock[location.Line] = true
		}
		out = append(out, finding{file: rel, line: location.Line, column: location.Column, form: form})
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
		case *ast.SelectorExpr:
			// safety: a call reaches this node through its CallExpr first and
			// claims the line, so what lands here is the timer taken as a
			// value, which a test calls out of sight of this walk.
			if name, ok := pkgCall(node, timePkg); ok && bannedCalls[name] {
				add(node.Pos(), "time."+name+" taken as a value")
			}
		}
		return true
	})
	return out, nil
}

// safety: a dot import puts Sleep in scope unqualified, and an unqualified
// name is indistinguishable from the test's own helper without type
// information this walk does not carry.
func importAlias(file *ast.File, path string) (alias string, dot bool) {
	for _, spec := range file.Imports {
		value, err := strconv.Unquote(spec.Path.Value)
		if err != nil || value != path {
			continue
		}
		if spec.Name == nil {
			return path[strings.LastIndex(path, "/")+1:], false
		}
		if spec.Name.Name == "." {
			return "", true
		}
		if spec.Name.Name == "_" {
			return "", false
		}
		return spec.Name.Name, false
	}
	return "", false
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
			if !callsElapsed(expr, timePkg) {
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
			if name, ok := pkgCall(node.Fun, timePkg); ok && clockReads[name] {
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

func callsElapsed(expr ast.Expr, timePkg string) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return !found
		}
		if name, ok := pkgCall(call.Fun, timePkg); ok && (name == "Since" || name == "Until") {
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

func report(findings []finding) {
	for _, f := range findings {
		fmt.Printf("%s:%d: %s. Instead: %s.\n", f.file, f.line, f.form, alternatives)
	}
	fmt.Printf("\nsleepcheck: %d test(s) wait on the wall clock.\n\n", len(findings))
	fmt.Print(advice)
}

// safety: a file the parser rejected carries no verdict, and a run that passes
// it reports health it never established.
func unreadableFailure(unread []unreadable) string {
	lines := make([]string, len(unread))
	for i, u := range unread {
		// safety: a go/parser error already opens with file:line:col, so naming
		// the file again would print it twice.
		text := u.err.Error()
		if !strings.Contains(text, u.file) {
			text = u.file + ": " + text
		}
		lines[i] = text
	}
	sort.Strings(lines)
	return fmt.Sprintf("%s\n\nsleepcheck: %d test file(s) could not be parsed, so this run reached no verdict on them.\n"+
		"Fix: make each file parse, then run the gate again.\n", strings.Join(lines, "\n"), len(unread))
}

func sortFindings(findings []finding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].file != findings[j].file {
			return findings[i].file < findings[j].file
		}
		if findings[i].line != findings[j].line {
			return findings[i].line < findings[j].line
		}
		return findings[i].column < findings[j].column
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

// safety: an unparseable file outside the scope is not something this run
// claimed to judge. This asks whether the diff names the file, not whether it
// added lines: a change that only deletes lines is the one most likely to
// break parsing.
func onlyChanged(unread []unreadable, root string, added map[string]map[int]bool) []unreadable {
	var out []unreadable
	for _, u := range unread {
		if gatescope.Touched(added, root, u.file) {
			out = append(out, u)
		}
	}
	return out
}

func scopedAdds(root string, staged bool, base string) (map[string]map[int]bool, error) {
	return gatescope.AddedLines(root, staged, base, "*_test.go")
}
