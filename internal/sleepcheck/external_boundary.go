package main

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path/filepath"
)

type externalBoundary struct {
	file     string
	function string
	duration string
	marker   string
}

type sourcePosition struct {
	line   int
	column int
}

var approvedExternalBoundaries = []externalBoundary{
	{
		file: "internal/orchestrator/run_node_claim_refused_test.go", function: "TestRunNodeCommand_StopsWhenTheClaimRenewalIsRefused",
		duration: "4 * store.DispatchedHeartbeatInterval",
		marker:   "// sleepcheck:external-boundary real HTTP claim renewal must arrive before pod cleanup",
	},
	{
		file: "internal/orchestrator/run_node_claim_refused_test.go", function: "TestRunNodeCommand_StopsWhenTheClaimRenewalIsRefused",
		duration: "2 * store.DispatchedHeartbeatInterval",
		marker:   "// sleepcheck:external-boundary refusal must stop the real pod before cleanup",
	},
	{
		file: "pkg/controller/direct_source_e2e_test.go", function: "startRunner",
		duration: "10 * time.Second",
		marker:   "// sleepcheck:external-boundary external runner gets ten seconds to exit before kill",
	},
	{
		file: "pkg/controller/direct_source_e2e_test.go", function: "awaitSuccess",
		duration: "500 * time.Millisecond",
		marker:   "// sleepcheck:external-boundary external runner status needs a paced HTTP poll",
	},
}

func approvedWaits(root string, rules []externalBoundary) (map[string]map[sourcePosition]bool, []externalBoundary) {
	allowed := map[string]map[sourcePosition]bool{}
	var stale []externalBoundary
	for _, rule := range rules {
		path := filepath.Join(root, filepath.FromSlash(rule.file))
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			stale = append(stale, rule)
			continue
		}
		timePkg, dot := importAlias(file, "time")
		if dot || timePkg == "" {
			stale = append(stale, rule)
			continue
		}
		var matched []sourcePosition
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != rule.function || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				name, ok := pkgCall(call.Fun, timePkg)
				if !ok || name != "After" || len(call.Args) != 1 || normalizedDuration(fset, call.Args[0]) != rule.duration {
					return true
				}
				position := fset.Position(call.Pos())
				for _, group := range file.Comments {
					if len(group.List) == 1 && group.List[0].Text == rule.marker && fset.Position(group.End()).Line == position.Line-1 {
						matched = append(matched, sourcePosition{position.Line, position.Column})
					}
				}
				return true
			})
		}
		if len(matched) != 1 {
			stale = append(stale, rule)
			continue
		}
		if allowed[rule.file] == nil {
			allowed[rule.file] = map[sourcePosition]bool{}
		}
		allowed[rule.file][matched[0]] = true
	}
	return allowed, stale
}

func normalizedDuration(fset *token.FileSet, expr ast.Expr) string {
	var text bytes.Buffer
	if err := format.Node(&text, fset, expr); err != nil {
		return ""
	}
	return text.String()
}

func withoutApproved(findings []finding, allowed map[string]map[sourcePosition]bool) []finding {
	var out []finding
	for _, f := range findings {
		if !allowed[f.file][sourcePosition{f.line, f.column}] {
			out = append(out, f)
		}
	}
	return out
}
