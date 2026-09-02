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
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/gitenv"
)

var tagRE = regexp.MustCompile(`(?i)^// ?(hack|safety|bug|perf):[[:space:]]*\S`)

var outputRE = regexp.MustCompile(`(?i)^// (Unordered output|Output):`)

var nosecRE = regexp.MustCompile(`^// ?#nosec G\d{3}(,G\d{3})* -- \S`)

var opaqueTicketRE = regexp.MustCompile(`(?i)\bBW-\d+\b`)

var skipDirs = map[string]bool{
	"vendor":          true,
	"testdata":        true,
	"node_modules":    true,
	".git":            true,
	".claude-scratch": true,
}

type violation struct {
	file string
	line int
	text string
}

func main() {
	staged := flag.Bool("staged", false, "only report comments in the staged diff (the pre-commit gate)")
	base := flag.String("base", "", "only report comments added vs the fork point from this git ref")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: commentcheck [-staged | -base ref] <root>")
		os.Exit(2)
	}
	root := flag.Arg(0)

	violations, err := scan(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "commentcheck:", err)
		os.Exit(2)
	}

	if *staged || *base != "" {
		added, aerr := scopedAdds(root, *staged, *base)
		if aerr != nil {
			fmt.Fprintf(os.Stderr, "commentcheck: cannot compute diff (%v); skipping gate\n", aerr)
			fmt.Println("commentcheck: skipped (no diff)")
			return
		}
		violations = onlyAdded(violations, root, added)
	}

	if len(violations) > 0 {
		report(violations)
		os.Exit(1)
	}
	fmt.Println("commentcheck: clean")
}

func scan(root string) ([]violation, error) {
	var violations []violation
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
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		v, perr := checkFile(path)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "commentcheck: skipping %s: %v\n", path, perr)
			return nil
		}
		violations = append(violations, v...)
		return nil
	})
	return violations, err
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
			if d.Name != nil && strings.HasPrefix(d.Name.Name, "Example") && d.Body != nil {
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
		return "a nosec annotation stands alone on one line"
	}
	text := cg.List[0].Text
	if !nosecRE.MatchString(text) {
		return "a nosec annotation reads #nosec GNNN -- reason"
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
	args := []string{"diff", "--unified=0", "--no-color"}
	var index string
	if staged {
		index = stagedIndex()
		args = append(args, "--cached")
	} else {
		forkPoint := base
		if out, err := git(root, "merge-base", base, "HEAD"); err == nil {
			forkPoint = strings.TrimSpace(out)
		}
		args = append(args, forkPoint)
	}
	args = append(args, "--", "*.go")

	diff, err := gitWithIndex(root, index, args...)
	if err != nil {
		return nil, err
	}
	return parseAddedLines(diff), nil
}

func stagedIndex() string {
	if os.Getenv("GIT_INDEX_FILE") != "" {
		return ""
	}
	return gitenv.GateIndex()
}

func parseAddedLines(diff string) map[string]map[int]bool {
	hunkRE := regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)
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
			start, _ := strconv.Atoi(m[1])
			count := 1
			if m[2] != "" {
				count, _ = strconv.Atoi(m[2])
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

func onlyAdded(violations []violation, root string, added map[string]map[int]bool) []violation {
	var out []violation
	for _, v := range violations {
		rel, err := filepath.Rel(root, v.file)
		if err != nil {
			rel = v.file
		}
		if added[rel][v.line] {
			out = append(out, v)
		}
	}
	return out
}

func git(root string, args ...string) (string, error) {
	return gitWithIndex(root, "", args...)
}

func gitWithIndex(root, index string, args ...string) (string, error) {
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
	fmt.Println("Allowed: GoDoc on exported API declarations and fields, plus")
	fmt.Println("  // hack:   a necessary deviation from the obvious approach")
	fmt.Println("  // safety: an invariant that isn't visible locally")
	fmt.Println("  // bug:    a known defect that remains unresolved")
	fmt.Println("  // perf:   a non-obvious optimization")
	fmt.Println("  // #nosec GNNN -- why the scanner finding is not a defect (one line, alone in its group)")
	fmt.Println("Fix: delete the comment, document the exported API, or tag the invariant.")
}
