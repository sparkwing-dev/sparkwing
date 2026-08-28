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

const generatedMarker = "<!-- GENERATED from the `sparkwing` package via go/doc (internal/sdkref). Do not edit by hand; regenerate with `bash bin/gen-sdk-docs.sh`. -->\n" +
	"<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->\n"

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

type subpackage struct {
	rel        string
	importPath string
	pkg        *doc.Package
}

func (s subpackage) importAlias() string { return "sw" + s.pkg.Name }

func (s subpackage) pageName() string {
	return "sdk-" + strings.TrimPrefix(s.rel, "sparkwing/") + ".md"
}

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

func writeValueBlocks(b *strings.Builder, fset *token.FileSet, vals []*doc.Value) {
	for _, v := range vals {
		b.WriteString("```\n" + decl(fset, v.Decl) + "\n```\n\n")
	}
}

func funcSig(fset *token.FileSet, fd *ast.FuncDecl) string {
	cp := *fd
	cp.Doc = nil
	cp.Body = nil
	return flatten(decl(fset, &cp))
}

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
