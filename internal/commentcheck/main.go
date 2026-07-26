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
//	commentcheck <root>              audit the whole tree; fail on any violation
//	commentcheck -staged <root>      fail only on comments in the staged diff
//	                                 (the pre-commit gate)
//	commentcheck -base <ref> <root>  fail only on comments added vs the fork
//	                                 point from <ref>
//
// The -staged and -base modes scope the gate to lines a change introduces, so
// the pre-existing comment corpus is never charged to a new commit. They fail
// open (warn and pass) if git can't produce the diff.
//
// Under a git hook the staged change lives in the index git is composing the
// commit in, not in the repository's own index, so -staged reads the one
// sparkwing put in SPARKWING_GATE_INDEX when it unbound the pipeline from the
// gated repository.
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

	"github.com/sparkwing-dev/sparkwing/internal/gitenv"
)

var tagRE = regexp.MustCompile(`(?i)^// ?(hack|safety|bug|perf):`)

// outputRE matches the Go testable-example output markers recognized by
// the testing package: "// Output:" and "// Unordered output:".
var outputRE = regexp.MustCompile(`(?i)^// (Unordered output|Output):`)

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
			mark(allowed, d.Doc)
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

// scopedAdds returns, per repo-relative path, the set of line numbers a change
// introduces. In staged mode that's the staged diff against HEAD; in base mode
// it's the diff against the merge-base with base, so lines that landed on base
// after the branch forked aren't charged to the branch.
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

// stagedIndex returns the index a staged read has to go through, or "" to use
// whichever index git would pick on its own. An inherited GIT_INDEX_FILE is a
// caller binding this run deliberately and wins; failing that, a hook-launched
// run reads the gate's index, because the repository's own index is stale under
// `git commit -a` and under a partial commit -- it would report an empty change
// and pass a commit nobody checked.
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

// gitWithIndex runs git against a named index file, or against the repository's
// own when index is empty. The binding is set on the one command rather than
// exported, so nothing else in the process tree can write through it.
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
	fmt.Println("Allowed: godoc on top-level declarations (and struct fields), plus")
	fmt.Println("  // hack:   deliberate deviation from the obvious approach")
	fmt.Println("  // safety: an invariant that isn't visible locally")
	fmt.Println("  // bug:    a known defect left in on purpose")
	fmt.Println("  // perf:   a non-obvious optimization")
	fmt.Println("Fix: delete the comment, move it onto the declaration as godoc, or tag it.")
}
