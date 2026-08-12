// sdkref generates docs/sdk-reference.md from the author-facing
// `sparkwing` package and its subpackages via go/doc: every exported
// function, type (with its exported fields), method, constant, and var,
// with its godoc synopsis. This is the same data pkg.go.dev renders, so
// the signature reference is derived from source and can't drift from
// the SDK -- unlike the hand-typed signature blocks it replaces in
// sdk.md.
//
// Subpackages (sparkwing/docker, sparkwing/git, ...) are part of the
// authoring surface: a pipeline that builds an image or reads the
// branch imports them. Documenting only the root package left them
// reachable exclusively by reading the module cache.
//
// Usage: go run . <repo-root> <out-dir>
//
// The reference is written as sdk-reference.md (the root package plus a
// linked subpackage index) and one sdk-<name>.md per subpackage. It was
// one page until that page reached 103K characters, past the ~100K
// truncation limit of most agent fetch tooling, which silently ate the
// subpackages at the end -- exactly the material an author reaches for
// when a pipeline builds an image or reads the branch.
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const importPath = "github.com/sparkwing-dev/sparkwing/sparkwing"

// generatedMarker opens every page this generator writes. Its first line
// doubles as the machine-readable "generated, not hand-authored" signal:
// doccheck skips prose-style gates on pages carrying it, and the split
// writer only prunes stale sdk-*.md pages that carry it.
const generatedMarker = "<!-- GENERATED from the `sparkwing` package via go/doc (internal/sdkref). Do not edit by hand; regenerate with `bash bin/gen-sdk-docs.sh`. -->\n" +
	"<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->\n"

// markerPrefix is the pruning guard: enough of generatedMarker to
// identify this generator's output and nothing else.
const markerPrefix = "<!-- GENERATED from the `sparkwing` package via go/doc"

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: sdkref <repo-root> <out-dir>")
		os.Exit(2)
	}
	root := filepath.Join(os.Args[1], "sparkwing")
	outDir := os.Args[2]

	fset := token.NewFileSet()
	dpkg, err := loadPackage(fset, root, importPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sdkref:", err)
		os.Exit(2)
	}
	subs, err := loadSubpackages(fset, root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sdkref:", err)
		os.Exit(2)
	}

	if err := writeSplit(outDir, fset, dpkg, subs); err != nil {
		fmt.Fprintln(os.Stderr, "sdkref:", err)
		os.Exit(2)
	}
}

// splitPages renders the reference as the sdk-reference.md index (root
// package + a linked subpackage list) plus one sdk-<name>.md per
// subpackage, keyed by filename.
func splitPages(fset *token.FileSet, dpkg *doc.Package, subs []subpackage) map[string]string {
	files := make(map[string]string, len(subs)+1)

	var idx strings.Builder
	idx.WriteString(generatedMarker)
	idx.WriteString("# SDK API reference\n\n")
	idx.WriteString("Every exported symbol in the `sparkwing` package (the SDK you import " +
		"as `sw`), generated from source. Browse the same thing with cross-links " +
		"on pkg.go.dev: <https://pkg.go.dev/" + importPath + ">. For concepts and " +
		"usage examples, see [sdk.md](sdk.md).\n\n")
	if len(subs) > 0 {
		idx.WriteString("## Subpackages\n\n")
		idx.WriteString("Part of the authoring surface too -- a pipeline that builds an image " +
			"or reads the branch imports these. Each has its own page:\n\n")
		for _, sub := range subs {
			line := "- [`" + sub.rel + "`](" + sub.pageName() + ")"
			if s := flatten(sub.pkg.Synopsis(sub.pkg.Doc)); s != "" {
				line += " -- " + s
			}
			idx.WriteString(line + "\n")
		}
		idx.WriteString("\n")
	}
	renderPackage(&idx, fset, dpkg, "##")
	files["sdk-reference.md"] = strings.TrimRight(idx.String(), "\n") + "\n"

	for _, sub := range subs {
		var b strings.Builder
		b.WriteString(generatedMarker)
		b.WriteString("# SDK API reference: `" + sub.rel + "`\n\n")
		if s := sub.pkg.Synopsis(sub.pkg.Doc); s != "" {
			b.WriteString(s + "\n\n")
		}
		b.WriteString("Import as `" + sub.importAlias() + " \"" + sub.importPath + "\"`. " +
			"The root package and the other subpackages are indexed in " +
			"[sdk-reference.md](sdk-reference.md).\n\n")
		renderPackage(&b, fset, sub.pkg, "##")
		files[sub.pageName()] = strings.TrimRight(b.String(), "\n") + "\n"
	}
	return files
}

// writeSplit writes the split reference into dir, skipping
// byte-identical pages, and prunes sdk-*.md pages a previous run
// generated for subpackages that no longer exist. Only pages opening
// with this generator's marker are ever pruned, so a hand-authored page
// whose name happens to match sdk-*.md survives -- docs/sdk.md does not
// match the pattern at all.
func writeSplit(dir string, fset *token.FileSet, dpkg *doc.Package, subs []subpackage) error {
	files := splitPages(fset, dpkg, subs)

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var wrote, unchanged, removed int
	for _, name := range names {
		path := filepath.Join(dir, name)
		if prev, err := os.ReadFile(path); err == nil && string(prev) == files[name] {
			unchanged++
			continue
		}
		if err := os.WriteFile(path, []byte(files[name]), 0o644); err != nil {
			return err
		}
		wrote++
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "sdk-") || !strings.HasSuffix(name, ".md") {
			continue
		}
		if _, ok := files[name]; ok {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(dir, name))
		if rerr != nil || !strings.HasPrefix(string(data), markerPrefix) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return err
		}
		fmt.Printf("removed stale generated page %s\n", filepath.Join(dir, name))
		removed++
	}

	fmt.Printf("sdk reference: wrote %d, unchanged %d, removed %d page(s) in %s\n", wrote, unchanged, removed, dir)
	return nil
}

// renderPackage writes one package's exported surface under heading
// level h (h+"#" for the per-type headings nested inside it).
func renderPackage(b *strings.Builder, fset *token.FileSet, dpkg *doc.Package, h string) {
	if len(dpkg.Funcs) > 0 {
		b.WriteString(h + " Functions\n\n")
		for _, f := range dpkg.Funcs {
			b.WriteString(symbolLine(dpkg, fset, f.Decl, f.Doc))
		}
		b.WriteString("\n")
	}

	if len(dpkg.Types) > 0 {
		b.WriteString(h + " Types\n\n")
		for _, t := range dpkg.Types {
			b.WriteString(h + "# type " + t.Name + "\n\n")
			if s := dpkg.Synopsis(t.Doc); s != "" {
				b.WriteString(s + "\n\n")
			}
			b.WriteString("```\n" + decl(fset, t.Decl) + "\n```\n\n")
			writeValueBlocks(b, fset, t.Consts)
			writeValueBlocks(b, fset, t.Vars)
			for _, c := range t.Funcs {
				b.WriteString(symbolLine(dpkg, fset, c.Decl, c.Doc))
			}
			for _, m := range t.Methods {
				b.WriteString(symbolLine(dpkg, fset, m.Decl, m.Doc))
			}
			b.WriteString("\n")
		}
	}

	writeValues(b, fset, h, "Constants", dpkg.Consts)
	writeValues(b, fset, h, "Variables", dpkg.Vars)
}

// subpackage is one importable package under sparkwing/, e.g.
// sparkwing/docker.
type subpackage struct {
	rel        string // "sparkwing/docker"
	importPath string
	pkg        *doc.Package
}

// importAlias is the name authors bind the package to. The repo's own
// pipelines alias these (swdocker, swgit) because the bare package name
// collides with the upstream library it wraps.
func (s subpackage) importAlias() string { return "sw" + s.pkg.Name }

// pageName is the subpackage's own page in the split reference, e.g.
// sparkwing/docker -> sdk-docker.md. The sdk- prefix keeps every
// generated page adjacent to sdk-reference.md in a directory listing
// and distinct from the hand-written sdk.md.
// Derived from the directory rather than the package name, so two
// packages that declare the same name can't collide on one page.
func (s subpackage) pageName() string {
	return "sdk-" + strings.TrimPrefix(s.rel, "sparkwing/") + ".md"
}

// loadPackage parses every non-test .go file in dir into a doc.Package.
func loadPackage(fset *token.FileSet, dir, ipath string) (*doc.Package, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", dir, err)
	}
	var files []*ast.File
	for _, m := range matches {
		if strings.HasSuffix(m, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, m, nil, parser.ParseComments)
		if perr != nil {
			return nil, fmt.Errorf("parse: %w", perr)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil, nil
	}
	return doc.NewFromFiles(fset, files, ipath)
}

// loadSubpackages returns every importable package directly under the
// SDK root, sorted by name.
//
// These are part of the authoring surface -- a pipeline that builds an
// image or reads the branch imports sparkwing/docker or sparkwing/git --
// but documenting only the root package left them invisible, so the only
// way to learn their API was to open the module cache.
func loadSubpackages(fset *token.FileSet, root string) ([]subpackage, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	var subs []subpackage
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ipath := importPath + "/" + e.Name()
		pkg, perr := loadPackage(fset, filepath.Join(root, e.Name()), ipath)
		if perr != nil {
			return nil, perr
		}
		if pkg == nil || (len(pkg.Funcs) == 0 && len(pkg.Types) == 0) {
			continue
		}
		subs = append(subs, subpackage{rel: "sparkwing/" + e.Name(), importPath: ipath, pkg: pkg})
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].rel < subs[j].rel })
	return subs, nil
}

// symbolLine renders one func/method as a list item: signature in
// backticks plus its one-line synopsis.
func symbolLine(dpkg *doc.Package, fset *token.FileSet, fd *ast.FuncDecl, godoc string) string {
	line := "- `" + funcSig(fset, fd) + "`"
	if s := flatten(dpkg.Synopsis(godoc)); s != "" {
		line += " -- " + s
	}
	return line + "\n"
}

func writeValues(b *strings.Builder, fset *token.FileSet, h, title string, vals []*doc.Value) {
	if len(vals) == 0 {
		return
	}
	b.WriteString(h + " " + title + "\n\n")
	writeValueBlocks(b, fset, vals)
}

// writeValueBlocks renders each const/var group as its own code block.
//
// go/doc files a constant declared with a named type under that type
// (doc.Type.Consts) rather than under doc.Package.Consts, so the type
// loop calls this too. Rendering only the package-level set drops every
// enum value in the SDK, leaving a reader who has just been shown
// `OnExpiry ApprovalTimeoutPolicy` no way to learn what to assign it.
func writeValueBlocks(b *strings.Builder, fset *token.FileSet, vals []*doc.Value) {
	for _, v := range vals {
		b.WriteString("```\n" + decl(fset, v.Decl) + "\n```\n\n")
	}
}

// funcSig prints a function/method signature without its body or
// leading godoc (a shallow copy so the shared AST isn't mutated).
func funcSig(fset *token.FileSet, fd *ast.FuncDecl) string {
	cp := *fd
	cp.Doc = nil
	cp.Body = nil
	return flatten(decl(fset, &cp))
}

// decl prints a node as Go source. go/doc has already stripped
// unexported struct fields / interface methods, so a printed struct
// shows only the exported surface (plus a "// Has unexported fields."
// marker). Comments live on the File, not sub-nodes, so they are not
// re-printed here.
// sourcePrinter emits space indentation (not tabs) so the generated
// code blocks pass markdownlint's no-hard-tabs rule.
var sourcePrinter = &printer.Config{Mode: printer.UseSpaces, Tabwidth: 4}

func decl(fset *token.FileSet, node ast.Node) string {
	if gd, ok := node.(*ast.GenDecl); ok {
		cp := *gd
		cp.Doc = nil
		node = &cp
	}
	var b bytes.Buffer
	_ = sourcePrinter.Fprint(&b, fset, node)
	return b.String()
}

func flatten(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
