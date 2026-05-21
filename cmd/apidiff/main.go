// apidiff emits a deterministic text snapshot of the exported API
// surface for every covered package (per VERSIONING.md), one file
// per package under the output directory.
//
// Usage:
//
//	go run ./cmd/apidiff <out-dir>
//
// The lint pipeline regenerates snapshots into a tempdir and diffs
// them against the checked-in .apidiff/ tree. Drift fails CI; the
// developer fixes it by running bash bin/regen-api-snapshot.sh and
// committing the updated snapshots in the same PR as the API change.
//
// Format notes:
//   - One file per package, named with slashes replaced by underscores
//     (`pkg/storage/fs.txt` -> `pkg_storage_fs.txt`).
//   - Godoc is intentionally NOT included -- comments rot faster than
//     APIs and Layer 4 already covers their accuracy.
//   - Output is stable: declarations sorted by name, methods grouped
//     under their receiver type, no iteration-order surprises.
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// packagePaths is the covered surface from VERSIONING.md:
// sparkwing (top-level SDK only) + every pkg/... and its public
// subpackages.
var packagePaths = []string{
	"sparkwing",
	"pkg/backends",
	"pkg/color",
	"pkg/controller",
	"pkg/controller/client",
	"pkg/controller/pool",
	"pkg/docs",
	"pkg/localws",
	"pkg/logs",
	"pkg/pipelines",
	"pkg/runner",
	"pkg/runners",
	"pkg/sources",
	"pkg/storage",
	"pkg/storage/fs",
	"pkg/storage/s3",
	"pkg/storage/sparkwingcache",
	"pkg/storage/sparkwinglogs",
	"pkg/storage/stdoutlogs",
	"pkg/storage/storeurl",
	"pkg/store",
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: apidiff <out-dir>")
		os.Exit(2)
	}
	outDir := os.Args[1]
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		die("mkdir %s: %v", outDir, err)
	}
	root, err := repoRoot()
	if err != nil {
		die("locate repo root: %v", err)
	}
	for _, p := range packagePaths {
		snap, err := snapshotPackage(filepath.Join(root, p), p)
		if err != nil {
			die("%s: %v", p, err)
		}
		outName := strings.ReplaceAll(p, "/", "_") + ".txt"
		if err := os.WriteFile(filepath.Join(outDir, outName), []byte(snap), 0o644); err != nil {
			die("write %s: %v", outName, err)
		}
	}
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "apidiff: "+format+"\n", args...)
	os.Exit(1)
}

// repoRoot walks up from cwd until it finds a go.mod whose module
// line names the sparkwing repo. Keeps the tool insensitive to where
// `go run` is invoked from.
func repoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		modPath := filepath.Join(dir, "go.mod")
		if body, err := os.ReadFile(modPath); err == nil {
			if bytes.Contains(body, []byte("module github.com/sparkwing-dev/sparkwing\n")) {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not inside sparkwing repo (started at %s)", cwd)
		}
		dir = parent
	}
}

type member struct {
	name string
	kind string // "const", "var", "type", "func", "method"
	text string
	recv string // for methods: bare receiver type name (no leading "*")
}

func snapshotPackage(dir, importPath string) (string, error) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	sort.Strings(files)

	var members []member
	for _, f := range files {
		astFile, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			return "", fmt.Errorf("parse %s: %w", f, err)
		}
		for _, d := range astFile.Decls {
			switch dd := d.(type) {
			case *ast.GenDecl:
				members = append(members, collectGenDecl(fset, dd)...)
			case *ast.FuncDecl:
				if m, ok := collectFunc(fset, dd); ok {
					members = append(members, m)
				}
			}
		}
	}

	// Build output. Methods group under their receiver type; orphan
	// methods (receiver type not declared here -- shouldn't happen for
	// exported symbols, but guard against it) sort to the end.
	methodsByRecv := map[string][]member{}
	var primary []member
	for _, m := range members {
		if m.kind == "method" {
			methodsByRecv[m.recv] = append(methodsByRecv[m.recv], m)
		} else {
			primary = append(primary, m)
		}
	}
	sort.SliceStable(primary, func(i, j int) bool {
		if primary[i].name == primary[j].name {
			return primary[i].kind < primary[j].kind
		}
		return primary[i].name < primary[j].name
	})
	for k := range methodsByRecv {
		ms := methodsByRecv[k]
		sort.SliceStable(ms, func(i, j int) bool { return ms[i].name < ms[j].name })
		methodsByRecv[k] = ms
	}

	var out bytes.Buffer
	fmt.Fprintf(&out, "# %s\n\n", importPath)
	emitted := map[string]bool{}
	for _, m := range primary {
		if emitted[m.kind+":"+m.name] {
			continue
		}
		emitted[m.kind+":"+m.name] = true
		out.WriteString(m.text)
		out.WriteString("\n")
		if m.kind == "type" {
			for _, mm := range methodsByRecv[m.name] {
				out.WriteString(mm.text)
				out.WriteString("\n")
			}
			delete(methodsByRecv, m.name)
		}
	}
	if len(methodsByRecv) > 0 {
		var orphanRecvs []string
		for k := range methodsByRecv {
			orphanRecvs = append(orphanRecvs, k)
		}
		sort.Strings(orphanRecvs)
		out.WriteString("\n# methods on un-declared receiver types (likely a bug)\n")
		for _, r := range orphanRecvs {
			for _, m := range methodsByRecv[r] {
				out.WriteString(m.text)
				out.WriteString("\n")
			}
		}
	}
	return out.String(), nil
}

func collectGenDecl(fset *token.FileSet, gd *ast.GenDecl) []member {
	var out []member
	for _, spec := range gd.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if !s.Name.IsExported() {
				continue
			}
			text := renderType(fset, s)
			out = append(out, member{name: s.Name.Name, kind: "type", text: text})
		case *ast.ValueSpec:
			tok := gd.Tok
			for i, n := range s.Names {
				if !n.IsExported() {
					continue
				}
				kw := "const"
				if tok == token.VAR {
					kw = "var"
				}
				out = append(out, member{
					name: n.Name,
					kind: kw,
					text: renderValueSpec(fset, tok, n, s, i),
				})
			}
		}
	}
	return out
}

func collectFunc(fset *token.FileSet, fd *ast.FuncDecl) (member, bool) {
	if !fd.Name.IsExported() {
		return member{}, false
	}
	text := renderFunc(fset, fd)
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		recv := receiverName(fd.Recv.List[0].Type)
		if recv == "" {
			return member{}, false
		}
		return member{name: fd.Name.Name, kind: "method", text: text, recv: recv}, true
	}
	return member{name: fd.Name.Name, kind: "func", text: text}, true
}

func receiverName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return receiverName(t.X)
	case *ast.IndexExpr:
		return receiverName(t.X)
	case *ast.IndexListExpr:
		return receiverName(t.X)
	}
	return ""
}

func renderFunc(fset *token.FileSet, fd *ast.FuncDecl) string {
	cp := *fd
	cp.Body = nil
	cp.Doc = nil
	var b bytes.Buffer
	_ = format.Node(&b, fset, &cp)
	return b.String()
}

func renderType(fset *token.FileSet, ts *ast.TypeSpec) string {
	cp := *ts
	cp.Doc = nil
	cp.Comment = nil
	cp.Type = filterUnexportedFields(cp.Type)
	gd := &ast.GenDecl{Tok: token.TYPE, Specs: []ast.Spec{&cp}}
	var b bytes.Buffer
	_ = format.Node(&b, fset, gd)
	return b.String()
}

// filterUnexportedFields strips unexported fields from struct types
// so the snapshot reflects only the public contract. Interface,
// function, and other type expressions pass through unchanged
// (interface methods are by definition all callable when the
// interface is exported).
func filterUnexportedFields(expr ast.Expr) ast.Expr {
	st, ok := expr.(*ast.StructType)
	if !ok || st.Fields == nil {
		return expr
	}
	var keep []*ast.Field
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			// embedded field: include if the embedded type is exported.
			if isExportedEmbedded(f.Type) {
				f2 := *f
				f2.Tag = nil
				f2.Doc = nil
				f2.Comment = nil
				keep = append(keep, &f2)
			}
			continue
		}
		var names []*ast.Ident
		for _, n := range f.Names {
			if n.IsExported() {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			continue
		}
		f2 := *f
		f2.Names = names
		f2.Tag = nil
		f2.Doc = nil
		f2.Comment = nil
		keep = append(keep, &f2)
	}
	stCp := *st
	stCp.Fields = &ast.FieldList{Opening: st.Fields.Opening, Closing: st.Fields.Closing, List: keep}
	return &stCp
}

func isExportedEmbedded(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.IsExported()
	case *ast.StarExpr:
		return isExportedEmbedded(t.X)
	case *ast.SelectorExpr:
		// e.g. sparkwing.Base -- always exported if Sel is exported.
		return t.Sel.IsExported()
	case *ast.IndexExpr:
		return isExportedEmbedded(t.X)
	case *ast.IndexListExpr:
		return isExportedEmbedded(t.X)
	}
	return false
}

func renderValueSpec(fset *token.FileSet, tok token.Token, name *ast.Ident, spec *ast.ValueSpec, i int) string {
	newSpec := &ast.ValueSpec{Names: []*ast.Ident{name}, Type: spec.Type}
	// Match name to value when both lists exist 1:1.
	if len(spec.Values) == len(spec.Names) {
		newSpec.Values = []ast.Expr{spec.Values[i]}
	}
	// Tuple assignment (one value, multiple names) and iota
	// continuation (no values) both leave newSpec.Values nil, which
	// renders as `const Name` / `var Name Type` -- the name is still
	// visible to the diff, which is what stability tracking needs.
	gd := &ast.GenDecl{Tok: tok, Specs: []ast.Spec{newSpec}}
	var b bytes.Buffer
	_ = format.Node(&b, fset, gd)
	return b.String()
}
