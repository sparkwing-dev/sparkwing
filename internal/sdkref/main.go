// sdkref generates docs/sdk-reference.md from the author-facing
// `sparkwing` package via go/doc: every exported function, type
// (with its exported fields), method, constant, and var, with its
// godoc synopsis. This is the same data pkg.go.dev renders, so the
// signature reference is derived from source and can't drift from the
// SDK -- unlike the hand-typed signature blocks it replaces in sdk.md.
//
// Usage: go run . <repo-root>   (writes markdown to stdout)
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
	"strings"
)

const importPath = "github.com/sparkwing-dev/sparkwing/sparkwing"

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: sdkref <repo-root>")
		os.Exit(2)
	}
	pkgDir := filepath.Join(os.Args[1], "sparkwing")

	fset := token.NewFileSet()
	matches, err := filepath.Glob(filepath.Join(pkgDir, "*.go"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "sdkref glob:", err)
		os.Exit(2)
	}
	var files []*ast.File
	for _, m := range matches {
		if strings.HasSuffix(m, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, m, nil, parser.ParseComments)
		if perr != nil {
			fmt.Fprintln(os.Stderr, "sdkref parse:", perr)
			os.Exit(2)
		}
		files = append(files, f)
	}
	dpkg, err := doc.NewFromFiles(fset, files, importPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sdkref doc:", err)
		os.Exit(2)
	}

	var b strings.Builder
	b.WriteString("<!-- GENERATED from the `sparkwing` package via go/doc (internal/sdkref). Do not edit by hand; regenerate with `bash bin/gen-sdk-docs.sh`. -->\n")
	b.WriteString("<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->\n")
	b.WriteString("# SDK API reference\n\n")
	b.WriteString("Every exported symbol in the `sparkwing` package (the SDK you import " +
		"as `sw`), generated from source. Browse the same thing with cross-links " +
		"on pkg.go.dev: <https://pkg.go.dev/" + importPath + ">. For concepts and " +
		"usage examples, see [sdk.md](sdk.md).\n\n")

	if len(dpkg.Funcs) > 0 {
		b.WriteString("## Functions\n\n")
		for _, f := range dpkg.Funcs {
			b.WriteString(symbolLine(dpkg, fset, f.Decl, f.Doc))
		}
		b.WriteString("\n")
	}

	if len(dpkg.Types) > 0 {
		b.WriteString("## Types\n\n")
		for _, t := range dpkg.Types {
			b.WriteString("### type " + t.Name + "\n\n")
			if s := dpkg.Synopsis(t.Doc); s != "" {
				b.WriteString(s + "\n\n")
			}
			b.WriteString("```\n" + decl(fset, t.Decl) + "\n```\n\n")
			for _, c := range t.Funcs {
				b.WriteString(symbolLine(dpkg, fset, c.Decl, c.Doc))
			}
			for _, m := range t.Methods {
				b.WriteString(symbolLine(dpkg, fset, m.Decl, m.Doc))
			}
			b.WriteString("\n")
		}
	}

	writeValues(&b, fset, "Constants", dpkg.Consts)
	writeValues(&b, fset, "Variables", dpkg.Vars)

	fmt.Print(b.String())
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

func writeValues(b *strings.Builder, fset *token.FileSet, title string, vals []*doc.Value) {
	if len(vals) == 0 {
		return
	}
	b.WriteString("## " + title + "\n\n")
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
