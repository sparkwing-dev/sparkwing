package pipelinelint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// AnalyzeSource parses every non-test .go file directly under dir,
// finds each pipeline Plan method, and runs the source rules over its
// body. Findings are tagged with the Plan's receiver type name (the
// entrypoint). Parsing is AST-only: it never type-checks or builds, so
// it works against a pinned-SDK source tree without resolving imports.
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

// analysis carries the per-Plan-method context every rule needs.
type analysis struct {
	fset     *token.FileSet
	file     string
	typeName string
	imports  map[string]string // local package identifier -> import path
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

// run walks the Plan body once, dispatching each node to the rules.
// Function-literal bodies are pruned: their code runs at dispatch, not
// while the plan is built, so I/O and env reads there are idiomatic.
func (a *analysis) run(body *ast.BlockStmt) {
	if body == nil {
		return
	}
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

// --- rule: plan-io -------------------------------------------------------

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

// --- rule: plan-runtime-branch ------------------------------------------

func (a *analysis) checkRuntimeBranch(call *ast.CallExpr) {
	sel := selectorOf(call.Fun)
	if sel == nil {
		return
	}
	// IsLocal() on any receiver: a host-environment branch.
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

// --- rules over job-builder chains and Ref discards ---------------------

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

// checkChain inspects a job-builder method chain (sparkwing.Job(...)
// .Needs(...).Inline().Requires(...)) rooted at a job constructor.
func (a *analysis) checkChain(expr ast.Expr) {
	root, methods := unwindChain(expr)
	if root == nil || !a.isJobConstructor(root) {
		return
	}
	inline := false
	var labelCalls []*ast.CallExpr
	for _, m := range methods {
		switch methodName(m) {
		case "Inline":
			inline = true
		case "Requires", "Prefers":
			labelCalls = append(labelCalls, m)
		}
	}
	for _, lc := range labelCalls {
		for _, arg := range lc.Args {
			if lit, ok := stringLit(arg); ok && strings.TrimSpace(lit) == "" {
				a.add(RuleRunnerLabel, lc.Pos(),
					methodName(lc)+" was given a blank runner label, which matches no runner; drop it or supply a real label.")
			}
		}
	}
	if inline && len(labelCalls) > 0 {
		a.add(RuleRunnerLabel, labelCalls[0].Pos(),
			"job is Inline() (in-process) yet declares "+methodName(labelCalls[0])+"; a runner label can never be honored on an inline job.")
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
	case "Job", "JobFanOut", "JobApproval", "GroupJobs":
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

// --- AST helpers --------------------------------------------------------

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
	// Identify the *Plan parameter (the DAG builder) to avoid matching
	// unrelated methods named Plan.
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

// baseTypeName strips a leading pointer and package qualifier, returning
// the bare type identifier (e.g. *sparkwing.Plan -> "Plan").
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

// selectorOf returns the SelectorExpr a call's Fun resolves to,
// unwrapping generic instantiation (RefTo[T] -> IndexExpr). Returns nil
// when the call target is not pkg.Fn shaped.
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

// unwindChain decomposes a method chain into its root call (the
// left-most call, e.g. the job constructor) and the method calls applied
// to it, in source order.
func unwindChain(expr ast.Expr) (root *ast.CallExpr, methods []*ast.CallExpr) {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return nil, nil
	}
	for {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return call, methods
		}
		inner, ok := sel.X.(*ast.CallExpr)
		if !ok {
			return call, methods
		}
		methods = append([]*ast.CallExpr{call}, methods...)
		call = inner
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

// findRefCall returns the first RefTo / RefToLastRun call inside expr,
// matched structurally by name, without descending into function
// literals. Used to spot a Ref produced into a blank assignment.
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
