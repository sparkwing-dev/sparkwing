package main

import (
	"fmt"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

const modulePath = "github.com/sparkwing-dev/sparkwing"

type exported struct {
	pkg, name string
	obj       types.Object
	used      bool
}

// safety: the rules copy the 2026-10-14 design-quality census, so a count here
// compares directly with that review's unexport-candidates.tsv.
func unexportCandidates(repo string) (map[string]int, map[string][]string, error) {
	cfg := &packages.Config{Dir: repo, Tests: true, Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports |
		packages.NeedDeps | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, nil, err
	}
	var loadErrs []string
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			loadErrs = append(loadErrs, e.Error())
		}
	})
	if len(loadErrs) > 0 {
		return nil, nil, fmt.Errorf("loading packages: %s", strings.Join(loadErrs, "; "))
	}
	fset := pkgs[0].Fset
	at := func(p token.Pos) string { return fset.Position(p).String() }
	inTest := func(p token.Pos) bool { return strings.HasSuffix(fset.Position(p).Filename, "_test.go") }

	defs := map[string]*exported{}
	var ifaces []*types.Interface
	seenIface := map[*types.Interface]bool{}
	addIface := func(t types.Type) {
		if it, ok := t.(*types.Interface); ok && it.NumMethods() > 0 && !seenIface[it] {
			seenIface[it] = true
			ifaces = append(ifaces, it)
		}
	}
	visited := map[*types.Package]bool{}
	var walk func(*types.Package)
	walk = func(tp *types.Package) {
		if visited[tp] {
			return
		}
		visited[tp] = true
		for _, n := range tp.Scope().Names() {
			if tn, ok := tp.Scope().Lookup(n).(*types.TypeName); ok {
				addIface(tn.Type().Underlying())
			}
		}
		for _, i := range tp.Imports() {
			walk(i)
		}
	}
	for _, p := range pkgs {
		if p.Types != nil {
			walk(p.Types)
		}
		if p.TypesInfo != nil {
			for _, tv := range p.TypesInfo.Types {
				addIface(tv.Type)
			}
		}
		if p.ID != p.PkgPath || !strings.HasPrefix(p.PkgPath, modulePath) {
			continue
		}
		isMain := p.Name == "main"
		scope := p.Types.Scope()
		for _, n := range scope.Names() {
			o := scope.Lookup(n)
			if inTest(o.Pos()) || strings.HasSuffix(fset.Position(o.Pos()).Filename, ".pb.go") {
				continue
			}
			if isMain {
				continue
			}
			_, isType := o.(*types.TypeName)
			if o.Exported() {
				defs[at(o.Pos())] = &exported{pkg: p.PkgPath, name: n, obj: o}
			}
			if nt, ok := o.Type().(*types.Named); ok && isType {
				if _, isIface := nt.Underlying().(*types.Interface); isIface {
					continue
				}
				for i := range nt.NumMethods() {
					if m := nt.Method(i); m.Exported() {
						defs[at(m.Pos())] = &exported{pkg: p.PkgPath, name: n + "." + m.Name(), obj: m}
					}
				}
			}
		}
	}
	for _, p := range pkgs {
		if p.TypesInfo == nil || (strings.HasSuffix(p.ID, ".test") && !strings.Contains(p.ID, "[")) {
			continue
		}
		user := strings.TrimSuffix(p.PkgPath, "_test")
		for _, o := range p.TypesInfo.Uses {
			if o == nil || o.Pkg() == nil || !strings.HasPrefix(o.Pkg().Path(), modulePath) {
				continue
			}
			if f, ok := o.(*types.Func); ok {
				o = f.Origin()
			}
			if v, ok := o.(*types.Var); ok && v.IsField() {
				continue
			}
			if d := defs[at(o.Pos())]; d != nil && d.pkg != user {
				d.used = true
			}
		}
	}
	exposed := exposedTypes(defs, at)
	counts, names := map[string]int{}, map[string][]string{}
	for key, d := range defs {
		if d.used || !strings.HasPrefix(d.pkg, modulePath+"/internal/") {
			continue
		}
		if _, isType := d.obj.(*types.TypeName); isType && exposed[key] {
			continue
		}
		if m, ok := d.obj.(*types.Func); ok && strings.Contains(d.name, ".") && satisfiesInterface(m, ifaces) {
			continue
		}
		pkg := strings.TrimPrefix(d.pkg, modulePath+"/")
		counts[pkg]++
		names[pkg] = append(names[pkg], d.name)
	}
	for _, list := range names {
		sort.Strings(list)
	}
	return counts, names, nil
}

func satisfiesInterface(m *types.Func, ifaces []*types.Interface) bool {
	recv := m.Type().(*types.Signature).Recv().Type()
	base := recv
	if pt, ok := recv.(*types.Pointer); ok {
		base = pt.Elem()
	}
	ptr := types.NewPointer(base)
	for _, it := range ifaces {
		has := false
		for i := range it.NumMethods() {
			if it.Method(i).Name() == m.Name() {
				has = true
				break
			}
		}
		if has && (types.Implements(ptr, it) || types.Implements(base, it)) {
			return true
		}
	}
	return false
}

func exposedTypes(defs map[string]*exported, at func(token.Pos) string) map[string]bool {
	exposed := map[string]bool{}
	seen := map[types.Type]bool{}
	var mark func(types.Type, int)
	mark = func(t types.Type, depth int) {
		if t == nil || seen[t] || depth > 8 {
			return
		}
		seen[t] = true
		switch t := t.(type) {
		case *types.Named:
			if t.Obj().Pkg() != nil && strings.HasPrefix(t.Obj().Pkg().Path(), modulePath) {
				exposed[at(t.Obj().Pos())] = true
				if st, ok := t.Underlying().(*types.Struct); ok {
					for i := range st.NumFields() {
						if st.Field(i).Exported() {
							mark(st.Field(i).Type(), depth+1)
						}
					}
				}
				for i := range t.NumMethods() {
					if t.Method(i).Exported() {
						mark(t.Method(i).Type(), depth+1)
					}
				}
				if it, ok := t.Underlying().(*types.Interface); ok {
					for i := range it.NumMethods() {
						mark(it.Method(i).Type(), depth+1)
					}
				}
			}
			if ta := t.TypeArgs(); ta != nil {
				for i := range ta.Len() {
					mark(ta.At(i), depth+1)
				}
			}
		case *types.Pointer:
			mark(t.Elem(), depth)
		case *types.Slice:
			mark(t.Elem(), depth)
		case *types.Array:
			mark(t.Elem(), depth)
		case *types.Map:
			mark(t.Key(), depth)
			mark(t.Elem(), depth)
		case *types.Chan:
			mark(t.Elem(), depth)
		case *types.Signature:
			for i := range t.Params().Len() {
				mark(t.Params().At(i).Type(), depth)
			}
			for i := range t.Results().Len() {
				mark(t.Results().At(i).Type(), depth)
			}
		case *types.Struct:
			for i := range t.NumFields() {
				mark(t.Field(i).Type(), depth+1)
			}
		}
	}
	for _, d := range defs {
		if d.used {
			mark(d.obj.Type(), 0)
		}
	}
	return exposed
}
