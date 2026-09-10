package pipelinelint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

func AnalyzeSource(dir string) ([]Finding, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var findings []Finding
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil, perr
		}
		imports := importMap(file)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !isPlanMethod(fn) {
				continue
			}
			a := &analysis{fset: fset, file: path, typeName: receiverTypeName(fn), imports: imports}
			a.run(fn.Body)
			findings = append(findings, a.findings...)
		}
	}
	return findings, nil
}

type analysis struct {
	fset     *token.FileSet
	file     string
	typeName string
	imports  map[string]string
	builders map[string]string
	findings []Finding
}

func (a *analysis) add(rule string, pos token.Pos, msg string) {
	p := a.fset.Position(pos)
	a.findings = append(a.findings, Finding{
		Rule:     rule,
		Pipeline: a.typeName,
		Message:  msg,
		File:     a.file,
		Line:     p.Line,
		Col:      p.Column,
	})
}

func (a *analysis) run(body *ast.BlockStmt) {
	if body == nil {
		return
	}
	a.collectBuilders(body)
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		switch node := n.(type) {
		case *ast.CallExpr:
			a.checkPlanIO(node)
			a.checkRuntimeBranch(node)
		case *ast.SelectorExpr:
			a.checkRuntimeSelector(node)
		case *ast.AssignStmt:
			a.checkAssign(node)
		case *ast.ExprStmt:
			a.checkExprStmt(node)
		}
		return true
	})
}

// collectBuilders records which SDK constructor each local variable holds, so
// a chain split across statements is checked like a single expression.
func (a *analysis) collectBuilders(body *ast.BlockStmt) {
	a.builders = map[string]string{}
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		id, ok := as.Lhs[0].(*ast.Ident)
		if !ok || id.Name == "_" {
			return true
		}
		root, _, _ := a.unwindChain(as.Rhs[0])
		if root == nil || !a.isJobConstructor(root) {
			return true
		}
		a.builders[id.Name] = selectorOf(root.Fun).Sel.Name
		return true
	})
}

var sdkIOFuncs = map[string]struct{}{"Bash": {}, "Exec": {}, "Shell": {}}

var osIOFuncs = map[string]struct{}{
	"ReadFile": {}, "WriteFile": {}, "Open": {}, "OpenFile": {}, "Create": {},
	"Remove": {}, "RemoveAll": {}, "Mkdir": {}, "MkdirAll": {}, "ReadDir": {},
	"Stat": {}, "Rename": {}, "Chdir": {},
}

func (a *analysis) checkPlanIO(call *ast.CallExpr) {
	sel := selectorOf(call.Fun)
	if sel == nil {
		return
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return
	}
	path, known := a.imports[pkg.Name]
	if !known {
		return
	}
	name := sel.Sel.Name
	flag := func() {
		a.add(RulePlanIO, call.Pos(),
			"Plan() must be pure-declarative: "+pkg.Name+"."+name+" is I/O and runs while the DAG is built. Move it into a Job or Step body (which runs at dispatch).")
	}
	switch {
	case isSDKPath(path):
		if _, hit := sdkIOFuncs[name]; hit {
			flag()
		}
	case strings.Contains(path, "/sparkwing/docker"), strings.Contains(path, "/sparkwing/git"):
		flag()
	case path == "os":
		if _, hit := osIOFuncs[name]; hit {
			flag()
		}
	case path == "os/exec":
		if name == "Command" || name == "CommandContext" {
			flag()
		}
	case path == "net/http":
		switch name {
		case "Get", "Post", "Head", "PostForm":
			flag()
		}
	case path == "io/ioutil":
		flag()
	}
}

func (a *analysis) checkRuntimeBranch(call *ast.CallExpr) {
	sel := selectorOf(call.Fun)
	if sel == nil {
		return
	}
	if sel.Sel.Name == "IsLocal" {
		a.add(RulePlanRuntimeBranch, call.Pos(),
			"Plan() must be deterministic: IsLocal() branches the DAG on where it runs. Use a job-level SkipIf / Requires or a pipeline guard instead.")
		return
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return
	}
	if a.imports[pkg.Name] == "os" && (sel.Sel.Name == "Getenv" || sel.Sel.Name == "LookupEnv") {
		a.add(RulePlanRuntimeBranch, call.Pos(),
			"Plan() must be deterministic: "+pkg.Name+"."+sel.Sel.Name+" reads the host environment while building the DAG. Move the condition to a job-level SkipIf / Requires or a pipeline guard.")
	}
}

func (a *analysis) checkRuntimeSelector(sel *ast.SelectorExpr) {
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return
	}
	if a.imports[pkg.Name] == "runtime" && (sel.Sel.Name == "GOOS" || sel.Sel.Name == "GOARCH") {
		a.add(RulePlanRuntimeBranch, sel.Pos(),
			"Plan() must be deterministic: runtime."+sel.Sel.Name+" branches the DAG on the build host. Express host targeting via a job-level Requires label instead.")
	}
}

func (a *analysis) checkAssign(as *ast.AssignStmt) {
	for _, rhs := range as.Rhs {
		a.checkChain(rhs)
	}
	if allBlank(as.Lhs) {
		for _, rhs := range as.Rhs {
			if refCall := findRefCall(rhs); refCall != nil {
				a.add(RuleUnusedRef, refCall.Pos(),
					"a Ref created here is discarded into _; wire it into a downstream job or drop the producing edge.")
			}
		}
	}
}

func (a *analysis) checkExprStmt(es *ast.ExprStmt) {
	a.checkChain(es.X)
	if call, ok := es.X.(*ast.CallExpr); ok && a.isRefCall(call) {
		a.add(RuleUnusedRef, call.Pos(),
			"a Ref is created as a bare statement and its result is unused; wire it into a downstream job or drop the producing edge.")
	}
}

func (a *analysis) checkChain(expr ast.Expr) {
	root, base, methods := a.unwindChain(expr)
	constructor := ""
	switch {
	case root != nil && a.isJobConstructor(root):
		constructor = selectorOf(root.Fun).Sel.Name
	case base != nil:
		constructor = a.builders[base.Name]
	}
	if constructor == "" {
		return
	}
	if constructor == "JobFanOutDynamic" {
		a.checkDynamicGroup(methods)
		return
	}
	inline := false
	var labelCalls, placementCalls []*ast.CallExpr
	for _, m := range methods {
		switch methodName(m) {
		case "Inline":
			inline = true
		case "Requires", "Prefers":
			labelCalls = append(labelCalls, m)
			placementCalls = append(placementCalls, m)
		case "WhenRunner":
			labelCalls = append(labelCalls, m)
		}
	}
	for _, lc := range labelCalls {
		for _, arg := range lc.Args {
			if lit, ok := stringLit(arg); ok && strings.TrimSpace(lit) == "" {
				a.add(RuleRunnerLabel, lc.Pos(),
					methodName(lc)+" was given a blank runner label: an empty string is dropped and a "+
						"whitespace label matches no runner, so the term either vanishes or can never be "+
						"satisfied. Drop it or supply a real label.")
			}
		}
	}
	if inline && len(placementCalls) > 0 {
		a.add(RuleRunnerLabel, placementCalls[0].Pos(),
			"job is Inline() (in-process) yet declares "+methodName(placementCalls[0])+"; a runner label can never be honored on an inline job.")
	}
	a.checkGroupCache(constructor, methods)
}

// groupReaders are the JobGroup methods that read the group rather than
// configure its members, so they mean the same thing on a dynamic group.
var groupReaders = map[string]struct{}{
	"Name": {}, "Members": {}, "Dynamic": {}, "Ready": {}, "Err": {},
}

func (a *analysis) checkDynamicGroup(methods []*ast.CallExpr) {
	for _, m := range methods {
		name := methodName(m)
		if _, read := groupReaders[name]; read || name == "" {
			continue
		}
		msg := name + "() on a JobFanOutDynamic group is dropped: every JobGroup setter applies to the " +
			"members present when it is called, and a dynamic group has none until its source completes. " +
			"Configure the generated jobs from the value the fan-out callback returns."
		if name == "Requires" || name == "Prefers" || name == "WhenRunner" {
			msg += " That Workable can declare its own labels through the " + name + "Provider interface."
		}
		a.add(RuleDynamicGroupInert, m.Pos(), msg)
	}
}

func (a *analysis) checkGroupCache(constructor string, methods []*ast.CallExpr) {
	switch constructor {
	case "JobFanOut", "GroupJobs":
	default:
		return
	}
	for _, m := range methods {
		if methodName(m) != "Memoize" {
			continue
		}
		a.add(RuleGroupCacheShared, m.Pos(),
			"Memoize() here applies one key to every member of "+constructor+
				", so the members share a cache entry and replay each other's results; "+
				"key them individually by ranging over the group's Members().")
	}
}

func (a *analysis) isJobConstructor(call *ast.CallExpr) bool {
	sel := selectorOf(call.Fun)
	if sel == nil {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || !isSDKPath(a.imports[pkg.Name]) {
		return false
	}
	switch sel.Sel.Name {
	case "Job", "JobFanOut", "JobFanOutDynamic", "JobApproval", "GroupJobs":
		return true
	}
	return false
}

func (a *analysis) isRefCall(call *ast.CallExpr) bool {
	sel := selectorOf(call.Fun)
	if sel == nil {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || !isSDKPath(a.imports[pkg.Name]) {
		return false
	}
	return sel.Sel.Name == "RefTo" || sel.Sel.Name == "RefToLastRun"
}

func importMap(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		name := ""
		if imp.Name != nil {
			name = imp.Name.Name
		} else {
			name = path[strings.LastIndex(path, "/")+1:]
		}
		if name == "_" || name == "." {
			continue
		}
		out[name] = path
	}
	return out
}

func isSDKPath(path string) bool {
	return path == "github.com/sparkwing-dev/sparkwing/sparkwing" || strings.HasSuffix(path, "/sparkwing/sparkwing")
}

func isPlanMethod(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || len(fn.Recv.List) == 0 || fn.Name.Name != "Plan" {
		return false
	}
	if fn.Type.Params == nil {
		return false
	}
	for _, field := range fn.Type.Params.List {
		star, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		if baseTypeName(star.X) == "Plan" {
			return true
		}
	}
	return false
}

func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	return baseTypeName(fn.Recv.List[0].Type)
}

func baseTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return baseTypeName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.Ident:
		return t.Name
	}
	return ""
}

func selectorOf(fun ast.Expr) *ast.SelectorExpr {
	switch f := fun.(type) {
	case *ast.SelectorExpr:
		return f
	case *ast.IndexExpr:
		return selectorOf(f.X)
	case *ast.IndexListExpr:
		return selectorOf(f.X)
	}
	return nil
}

// unwindChain walks a builder chain back to its start. It returns the rooting
// call for a chain written in one expression, or the identifier a chain
// continues from when the base is a variable rather than a package qualifier.
func (a *analysis) unwindChain(expr ast.Expr) (root *ast.CallExpr, base *ast.Ident, methods []*ast.CallExpr) {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return nil, nil, nil
	}
	for {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return call, nil, methods
		}
		switch inner := sel.X.(type) {
		case *ast.CallExpr:
			methods = append([]*ast.CallExpr{call}, methods...)
			call = inner
		case *ast.Ident:
			if _, pkg := a.imports[inner.Name]; pkg {
				return call, nil, methods
			}
			methods = append([]*ast.CallExpr{call}, methods...)
			return nil, inner, methods
		default:
			return call, nil, methods
		}
	}
}

func methodName(call *ast.CallExpr) string {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	return ""
}

func stringLit(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	return strings.Trim(lit.Value, "`\""), true
}

func allBlank(exprs []ast.Expr) bool {
	if len(exprs) == 0 {
		return false
	}
	for _, e := range exprs {
		id, ok := e.(*ast.Ident)
		if !ok || id.Name != "_" {
			return false
		}
	}
	return true
}

func findRefCall(expr ast.Expr) *ast.CallExpr {
	var found *ast.CallExpr
	ast.Inspect(expr, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if sel := selectorOf(call.Fun); sel != nil {
				if sel.Sel.Name == "RefTo" || sel.Sel.Name == "RefToLastRun" {
					found = call
					return false
				}
			}
		}
		return true
	})
	return found
}
