package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNodeSpecReadersDecodeArgumentValues(t *testing.T) {
	readers := map[string]string{
		"blob_read_authority.go:requireBlobMemoTx":  "decodeNodeSpec",
		"blob_read_authority.go:blobDependenciesTx": "decodeNodeSpec",
		"authority_issue.go:loadAwardRowTx":         "decodeNodeSpec",
		"store.go:ListDeclaredSteps":                "decodeNodeSpec",
		"state_read.go:scanNode":                    "decodeNodeSpec",
		"state_read.go:readNodeTx":                  "scanNode",
		"state_read.go:readSnapshotTx":              "scanNode",
		"sealed_rotation.go:expansionArguments":     "nodeArguments",
		"node_state.go:insertNodeTx":                "encodeNodeSpec",
		// safety: SQL evaluates only clear approval/closure metadata; it never reads argument defaults.
		"approval_evaluation.go:evaluationSourceSQL": "",
	}
	var quoted func(ast.Expr) string
	quoted = func(expr ast.Expr) string {
		switch value := expr.(type) {
		case *ast.BasicLit:
			if value.Kind == token.STRING {
				raw, _ := strconv.Unquote(value.Value)
				return raw
			}
		case *ast.BinaryExpr:
			if value.Op == token.ADD {
				return quoted(value.X) + quoted(value.Y)
			}
		}
		return ""
	}
	seen := map[string]bool{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Clean(name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			accessesSpec := false
			columns := false
			calls := map[string]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch value := n.(type) {
				case *ast.BasicLit:
					if value.Kind == token.STRING {
						raw, _ := strconv.Unquote(value.Value)
						text := strings.ToLower(raw)
						if fn.Name.Name == "evaluationSourceSQL" && (text == "arguments" || strings.Contains(text, "$.arguments")) {
							t.Error("approval SQL must not read encrypted argument defaults")
						}

						accessesSpec = accessesSpec || strings.Contains(text, "spec_json") && strings.Contains(text, "nodes") || strings.Contains(text, ".spec_json") && !strings.Contains(text, "r.spec_json")
					}
				case *ast.BinaryExpr:
					text := strings.ToLower(quoted(value))
					accessesSpec = accessesSpec || strings.Contains(text, "spec_json") && strings.Contains(text, "nodes")
				case *ast.Ident:
					columns = columns || value.Name == "nodeReadColumns"
				case *ast.CallExpr:
					switch callee := value.Fun.(type) {
					case *ast.Ident:
						calls[callee.Name] = true
					case *ast.SelectorExpr:
						calls[callee.Sel.Name] = true
					}
				}
				return true
			})
			access := fn.Name.Name == "scanNode" || columns || accessesSpec
			if !access {
				continue
			}
			key := name + ":" + fn.Name.Name
			decode, ok := readers[key]
			if !ok {
				t.Errorf("unreviewed node spec reader %s; route values through decodeNodeSpec and add its reader contract", key)
				continue
			}
			seen[key] = true
			if decode != "" && !calls[decode] {
				t.Errorf("%s bypasses %s", key, decode)
			}
		}
	}
	for reader := range readers {
		if !seen[reader] {
			t.Errorf("reader inventory is stale: %s", reader)
		}
	}
}
