package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: apidiff <out-dir>")
		os.Exit(2)
	}
	outDir := os.Args[1]
	// #nosec G703 -- a build-time tool reading paths the operator names
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		die("mkdir %s: %v", outDir, err)
	}
	root, err := repoRoot()
	if err != nil {
		die("locate repo root: %v", err)
	}
	packagePaths, err := discoverPackagePaths(root)
	if err != nil {
		die("discover public packages: %v", err)
	}
	for _, p := range packagePaths {
		snap, err := snapshotPackage(filepath.Join(root, p), p)
		if err != nil {
			die("%s: %v", p, err)
		}
		outName := strings.ReplaceAll(p, "/", "_") + ".txt"
		// #nosec G703 -- a build-time tool reading paths the operator names
		if err := os.WriteFile(filepath.Join(outDir, outName), []byte(snap), 0o644); err != nil {
			die("write %s: %v", outName, err)
		}
	}
}

func discoverPackagePaths(root string) ([]string, error) {
	packages := map[string]struct{}{"sparkwing": {}}
	pkgRoot := filepath.Join(root, "pkg")
	err := filepath.WalkDir(pkgRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		importPath := filepath.ToSlash(rel)
		if importPath == "internal" || strings.Contains(importPath, "/internal/") ||
			strings.HasSuffix(importPath, "/internal") {
			return nil
		}
		packages[importPath] = struct{}{}
		return nil
	})
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(packages))
	for path := range packages {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "apidiff: "+format+"\n", args...)
	os.Exit(1)
}

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
	kind string
	text string
	recv string
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

func filterUnexportedFields(expr ast.Expr) ast.Expr {
	st, ok := expr.(*ast.StructType)
	if !ok || st.Fields == nil {
		return expr
	}
	var keep []*ast.Field
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
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
	if len(spec.Values) == len(spec.Names) {
		newSpec.Values = []ast.Expr{spec.Values[i]}
	}
	gd := &ast.GenDecl{Tok: tok, Specs: []ast.Spec{newSpec}}
	var b bytes.Buffer
	_ = format.Node(&b, fset, gd)
	return b.String()
}
