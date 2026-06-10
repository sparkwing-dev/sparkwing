// Command commentcheck enforces the repo comment policy: comments are scarce
// and trustworthy, never narration that rots when the code moves out from
// under it.
//
// Two kinds of comment are allowed:
//
//   - godoc attached to a top-level declaration (package, func, type, const,
//     var, import) and to struct fields / interface methods. These render in
//     an editor and on pkg.go.dev, so they document a contract rather than
//     restating the code.
//
//   - a tiny allowlist of tagged implementation comments that force the
//     author to justify the comment's existence:
//
//     // hack:   a deliberate deviation from the obvious/correct approach
//     // safety: an invariant that must hold but isn't visible locally
//     // bug:    a known defect left in on purpose
//     // perf:   a non-obvious optimization worth defending
//
// Everything else -- free-floating comments, narration inside function
// bodies, section dividers, "what" comments that restate the code -- is
// rejected. Compiler directives (//go:build, //go:embed, //nolint:...) are
// always allowed regardless of position.
//
// A claim about another package's behavior belongs in a test or a type that
// fails loudly when it stops being true, never in prose that degrades
// silently. This tool can't see meaning, so it can't enforce that directly;
// it enforces scarcity, which collapses the surface where such claims hide.
//
// Usage:
//
//	commentcheck <root>             audit the whole tree; fail on any violation
//	commentcheck -base <ref> <root> fail only on comments this branch adds vs
//	                                the fork point from <ref> (the push gate)
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
)

var tagRE = regexp.MustCompile(`(?i)^// ?(hack|safety|bug|perf):`)

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
	base := flag.String("base", "", "only report comments added on this branch vs the fork point from this git ref")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: commentcheck [-base ref] <root>")
		os.Exit(2)
	}
	root := flag.Arg(0)

	violations, err := scan(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "commentcheck:", err)
		os.Exit(2)
	}

	if *base != "" {
		added, aerr := addedLines(root, *base)
		if aerr != nil {
			fmt.Fprintf(os.Stderr, "commentcheck: cannot diff against %s (%v); skipping gate\n", *base, aerr)
			fmt.Println("commentcheck: skipped (no diff base)")
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
			mark(allowed, d.Doc)
		case *ast.GenDecl:
			mark(allowed, d.Doc)
			for _, spec := range d.Specs {
				collectSpec(allowed, spec)
			}
		}
	}

	var out []violation
	for _, cg := range f.Comments {
		if allowed[cg] {
			continue
		}
		first := cg.List[0].Text
		if isDirective(first) || tagRE.MatchString(first) {
			continue
		}
		pos := fset.Position(cg.Pos())
		out = append(out, violation{pos.Filename, pos.Line, firstLine(first)})
	}
	return out, nil
}

// collectSpec marks godoc attached to a top-level spec and, for type specs,
// recurses into struct fields and interface methods so their godoc survives.
// It never descends into function bodies -- comments there are implementation
// comments and must earn their place through the tag allowlist.
func collectSpec(allowed map[*ast.CommentGroup]bool, spec ast.Spec) {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		mark(allowed, s.Doc)
		mark(allowed, s.Comment)
		collectType(allowed, s.Type)
	case *ast.ValueSpec:
		mark(allowed, s.Doc)
		mark(allowed, s.Comment)
	case *ast.ImportSpec:
		mark(allowed, s.Doc)
		mark(allowed, s.Comment)
	}
}

func collectType(allowed map[*ast.CommentGroup]bool, expr ast.Expr) {
	switch t := expr.(type) {
	case *ast.StructType:
		for _, fld := range t.Fields.List {
			mark(allowed, fld.Doc)
			mark(allowed, fld.Comment)
			collectType(allowed, fld.Type)
		}
	case *ast.InterfaceType:
		for _, m := range t.Methods.List {
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

func mark(allowed map[*ast.CommentGroup]bool, cg *ast.CommentGroup) {
	if cg != nil {
		allowed[cg] = true
	}
}

// isDirective reports whether a //-comment is a compiler directive such as
// //go:build, //go:embed, or //nolint:all -- the form is //word:rest with no
// space after the slashes. The required leading space in "// hack:" is what
// keeps human tags from being mistaken for directives, and vice versa.
func isDirective(text string) bool {
	s, ok := strings.CutPrefix(text, "//")
	if !ok || s == "" || s[0] == ' ' {
		return false
	}
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return false
	}
	for _, r := range s[:i] {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
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

// addedLines returns, per repo-relative path, the set of line numbers this
// branch added relative to the fork point from base. It diffs against the
// merge-base so lines that landed on base after the branch forked aren't
// charged to the branch.
func addedLines(root, base string) (map[string]map[int]bool, error) {
	forkPoint := base
	if out, err := git(root, "merge-base", base, "HEAD"); err == nil {
		forkPoint = strings.TrimSpace(out)
	}
	diff, err := git(root, "diff", "--unified=0", "--no-color", forkPoint, "--", "*.go")
	if err != nil {
		return nil, err
	}

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
	return added, nil
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
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
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
	fmt.Println("Allowed: godoc on top-level declarations (and struct fields), plus")
	fmt.Println("  // hack:   deliberate deviation from the obvious approach")
	fmt.Println("  // safety: an invariant that isn't visible locally")
	fmt.Println("  // bug:    a known defect left in on purpose")
	fmt.Println("  // perf:   a non-obvious optimization")
	fmt.Println("Fix: delete the comment, move it onto the declaration as godoc, or tag it.")
}
