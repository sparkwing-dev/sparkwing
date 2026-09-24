package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/gatescope"
)

var tagRE = regexp.MustCompile(`(?i)^// ?(hack|safety|bug|perf):[[:space:]]*\S`)

var outputRE = regexp.MustCompile(`(?i)^// (Unordered output|Output):`)

var nosecRE = regexp.MustCompile(`^// ?#nosec G\d{3}(,G\d{3})* -- \S`)

var opaqueTicketRE = regexp.MustCompile(`(?i)\bBW-\d+\b`)

type violation struct {
	file string
	line int
	text string
}

type unreadable struct {
	file string
	err  error
}

const usageText = `usage: commentcheck [-staged | -base ref] [-allow-no-diff] <root>

<root> is one directory to walk, normally the repository root; commentcheck
takes no list of files. It parses every .go file under <root> and skips
vendor, testdata, node_modules, .git and .claude-scratch. -staged and -base
narrow the report to the lines those diffs add; -base also reads untracked
.go files, which git diff alone leaves out.

Allowed: GoDoc on exported API declarations and fields, body comments tagged
hack:, safety:, bug: or perf:, #nosec GNNN -- reason annotations, and
sleepcheck:external-boundary markers checked by internal/sleepcheck.
Caps: a tagged comment runs at most 4 lines, each at most 120 characters; a
#nosec annotation is one line standing alone in its comment group.
A tag opens a // comment, so a /* */ block carries no tag and is a violation
wherever a tagged comment would be allowed. A .go file the parser rejects
fails the run, because a file the gate could not read is a file it did not
judge.

flags:
`

func usage() {
	fmt.Fprint(flag.CommandLine.Output(), usageText)
	flag.PrintDefaults()
}

func main() {
	staged := flag.Bool("staged", false, "only report comments in the staged diff (the pre-commit gate)")
	base := flag.String("base", "", "only report comments added vs the fork point from this git ref")
	allowNoDiff := flag.Bool("allow-no-diff", false, "pass instead of failing when the diff cannot be computed; the run gates nothing")
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() != 1 {
		usage()
		os.Exit(2)
	}
	root := flag.Arg(0)

	violations, unread, err := scan(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "commentcheck:", err)
		os.Exit(2)
	}

	scoped := -1
	if *staged || *base != "" {
		added, aerr := scopedAdds(root, *staged, *base)
		if aerr != nil {
			if !*allowNoDiff {
				fmt.Fprintln(os.Stderr, diffFailure(*base, aerr))
				os.Exit(2)
			}
			fmt.Fprintf(os.Stderr, "commentcheck: cannot compute the diff (%v); -allow-no-diff accepted a run that gates nothing\n", aerr)
			fmt.Println("commentcheck: skipped (no diff)")
			return
		}
		violations = onlyAdded(violations, root, added)
		unread = onlyChanged(unread, root, added)
		scoped = len(added)
	}

	if len(violations) > 0 {
		report(violations)
	}
	if len(unread) > 0 {
		fmt.Print(unreadableFailure(unread))
	}
	if len(violations) > 0 || len(unread) > 0 {
		os.Exit(1)
	}
	fmt.Println(cleanLine(scoped))
}

func diffFailure(base string, err error) string {
	return gatescope.DiffFailure("commentcheck", base, err)
}

func scan(root string) ([]violation, []unreadable, error) {
	var violations []violation
	var unread []unreadable
	err := gatescope.Walk(root, ".go", func(path, _ string) {
		v, perr := checkFile(path)
		if perr != nil {
			unread = append(unread, unreadable{file: path, err: perr})
			return
		}
		violations = append(violations, v...)
	})
	return violations, unread, err
}

func checkFile(path string) ([]violation, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	allowed := map[*ast.CommentGroup]bool{}
	mark(allowed, f.Doc)
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name != nil && d.Name.IsExported() {
				mark(allowed, d.Doc)
			}
			if strings.HasSuffix(path, "_test.go") && d.Name != nil && strings.HasPrefix(d.Name.Name, "Example") && d.Body != nil {
				bodyStart := d.Body.Lbrace
				bodyEnd := d.Body.Rbrace
				for _, cg := range f.Comments {
					if cg.Pos() >= bodyStart && cg.Pos() <= bodyEnd && outputRE.MatchString(cg.List[0].Text) {
						mark(allowed, cg)
					}
				}
			}
		case *ast.GenDecl:
			exported := false
			for _, spec := range d.Specs {
				exported = collectSpec(allowed, spec) || exported
			}
			if exported {
				mark(allowed, d.Doc)
			}
		}
	}

	var out []violation
	for _, cg := range f.Comments {
		if opaqueTicketRE.MatchString(cg.Text()) {
			pos := fset.Position(cg.Pos())
			out = append(out, violation{pos.Filename, pos.Line, firstLine(cg.List[0].Text)})
			continue
		}
		if allowed[cg] {
			continue
		}
		first := cg.List[0].Text
		if isDirective(first) {
			for _, comment := range cg.List[1:] {
				if isDirective(comment.Text) {
					continue
				}
				pos := fset.Position(comment.Pos())
				out = append(out, violation{pos.Filename, pos.Line, firstLine(comment.Text)})
			}
			continue
		}
		if strings.Contains(cg.Text(), "#nosec") {
			reason := nosecGroupViolation(cg)
			if reason == "" {
				continue
			}
			pos := fset.Position(cg.Pos())
			out = append(out, violation{pos.Filename, pos.Line, firstLine(first) + " (" + reason + ")"})
			continue
		}
		if tagRE.MatchString(first) {
			reason := tagGroupViolation(cg)
			if reason == "" {
				continue
			}
			pos := fset.Position(cg.Pos())
			out = append(out, violation{pos.Filename, pos.Line, firstLine(first) + " (" + reason + ")"})
			continue
		}
		pos := fset.Position(cg.Pos())
		out = append(out, violation{pos.Filename, pos.Line, firstLine(first)})
	}
	return out, nil
}

func nosecGroupViolation(cg *ast.CommentGroup) string {
	if len(cg.List) != 1 || strings.Contains(cg.List[0].Text, "\n") {
		return "nosec adjacency: a nosec annotation stands alone on one line, " +
			"because every other line of its group rides past this gate unread; " +
			"put a blank line between the annotation and the tagged comment above it"
	}
	text := cg.List[0].Text
	if !nosecRE.MatchString(text) {
		return "nosec form: a nosec annotation reads #nosec GNNN -- reason"
	}
	if utf8.RuneCountInString(text) > 120 {
		return "tagged comment lines are limited to 120 characters"
	}
	return ""
}

func tagGroupViolation(cg *ast.CommentGroup) string {
	lines := 0
	for _, comment := range cg.List {
		lines += strings.Count(comment.Text, "\n") + 1
	}
	if lines > 4 {
		return "tagged comments are limited to four lines"
	}
	for _, comment := range cg.List {
		for line := range strings.SplitSeq(comment.Text, "\n") {
			if utf8.RuneCountInString(line) > 120 {
				return "tagged comment lines are limited to 120 characters"
			}
		}
	}
	return ""
}

func collectSpec(allowed map[*ast.CommentGroup]bool, spec ast.Spec) bool {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		if !s.Name.IsExported() {
			return false
		}
		mark(allowed, s.Doc)
		mark(allowed, s.Comment)
		collectType(allowed, s.Type)
		return true
	case *ast.ValueSpec:
		exported := false
		for _, name := range s.Names {
			exported = exported || name.IsExported()
		}
		if !exported {
			return false
		}
		mark(allowed, s.Doc)
		mark(allowed, s.Comment)
		return true
	}
	return false
}

func collectType(allowed map[*ast.CommentGroup]bool, expr ast.Expr) {
	switch t := expr.(type) {
	case *ast.StructType:
		for _, fld := range t.Fields.List {
			if !fieldExported(fld) {
				continue
			}
			mark(allowed, fld.Doc)
			mark(allowed, fld.Comment)
			collectType(allowed, fld.Type)
		}
	case *ast.InterfaceType:
		for _, m := range t.Methods.List {
			if !fieldExported(m) {
				continue
			}
			mark(allowed, m.Doc)
			mark(allowed, m.Comment)
		}
	case *ast.StarExpr:
		collectType(allowed, t.X)
	case *ast.ArrayType:
		collectType(allowed, t.Elt)
	case *ast.MapType:
		collectType(allowed, t.Key)
		collectType(allowed, t.Value)
	}
}

func fieldExported(field *ast.Field) bool {
	for _, name := range field.Names {
		if name.IsExported() {
			return true
		}
	}
	if len(field.Names) != 0 {
		return false
	}
	name := embeddedFieldName(field.Type)
	return name != nil && name.IsExported()
}

func embeddedFieldName(expr ast.Expr) *ast.Ident {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr
	case *ast.SelectorExpr:
		return expr.Sel
	case *ast.StarExpr:
		return embeddedFieldName(expr.X)
	case *ast.IndexExpr:
		return embeddedFieldName(expr.X)
	case *ast.IndexListExpr:
		return embeddedFieldName(expr.X)
	case *ast.ParenExpr:
		return embeddedFieldName(expr.X)
	}
	return nil
}

func mark(allowed map[*ast.CommentGroup]bool, cg *ast.CommentGroup) {
	if cg != nil {
		allowed[cg] = true
	}
}

func isDirective(text string) bool {
	return strings.HasPrefix(text, "//go:") ||
		strings.HasPrefix(text, "//nolint:") ||
		strings.HasPrefix(text, "// sleepcheck:external-boundary ") ||
		strings.HasPrefix(text, "//lint:ignore ") ||
		strings.HasPrefix(text, "//lint:file-ignore ")
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if len(text) > 80 {
		text = text[:77] + "..."
	}
	return text
}

func scopedAdds(root string, staged bool, base string) (map[string]map[int]bool, error) {
	return gatescope.AddedLines(root, staged, base, "*.go")
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

func onlyAdded(violations []violation, root string, added map[string]map[int]bool) []violation {
	var out []violation
	for _, v := range violations {
		if gatescope.Added(added, root, v.file, v.line) {
			out = append(out, v)
		}
	}
	return out
}

func report(violations []violation) {
	lines := make([]string, len(violations))
	for i, v := range violations {
		lines[i] = fmt.Sprintf("%s:%d: disallowed comment: %s", v.file, v.line, v.text)
	}
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Println(l)
	}
	fmt.Printf("\ncommentcheck: %d disallowed comment(s).\n\n", len(violations))
	fmt.Print(advice)
}

// safety: a file the parser rejected carries no verdict, and a gate that
// passes it reports health it never established.
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
	return fmt.Sprintf("%s\n\ncommentcheck: %d file(s) could not be parsed, so this run reached no verdict on them.\n"+
		"Fix: make each file parse, then run the gate again.\n", strings.Join(lines, "\n"), len(unread))
}

const advice = `Allowed: GoDoc on exported API declarations and fields, plus
  // hack:   a necessary deviation from the obvious approach
  // safety: an invariant that isn't visible locally
  // bug:    a known defect that remains unresolved
  // perf:   a non-obvious optimization
  // #nosec GNNN -- why the scanner finding is not a defect (one line, alone in its group)
A tagged comment runs at most 4 lines, each at most 120 characters, and opens a
// comment; rewrite a /* */ block as // lines to tag it.
Fix: tag the comment, do not delete it. A body comment must start with one of
  hack:/safety:/bug:/perf: and say why in one short line. Rationale for a
  non-obvious choice is a why-comment and belongs under hack: or safety:,
  whichever fits; that knowledge is worth keeping. Delete only narration that
  restates what the code already says, and give an exported declaration a
  GoDoc comment rather than a tag.
`

// safety: a scoped run whose diff named no Go file reports nothing wrong
// because it read nothing, and a caller who cannot tell that from a pass
// trusts a verdict that was never reached.
func cleanLine(scoped int) string {
	switch {
	case scoped == 0:
		return "commentcheck: clean, but the diff named no Go file, so this run judged nothing"
	case scoped > 0:
		return fmt.Sprintf("commentcheck: clean across %d file(s)", scoped)
	default:
		return "commentcheck: clean"
	}
}
