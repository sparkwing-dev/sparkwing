package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestClaimantFactsHaveOneOwner(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if claimantRuleOutsideOwner(name, node) {
				t.Errorf("%s: claimant facts belong to claimant_facts.go; eligibility belongs to claim_query.go and claim_ranking.go", fset.Position(node.Pos()))
			}
			return true
		})
	}
}

func claimantRuleOutsideOwner(name string, node ast.Node) bool {
	if selector, ok := node.(*ast.SelectorExpr); ok && selector.Sel.Name == "Personal" {
		if origin, ok := selector.X.(*ast.SelectorExpr); ok && origin.Sel.Name == "Origin" {
			return name != "claimant_facts.go"
		}
	}
	literal, ok := node.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING || name == "claim_ranking.go" || name == "claim_query.go" {
		return false
	}
	value, _ := strconv.Unquote(literal.Value)
	switch value {
	case TierHere, TierPersonal, TierActions, TierSelfHosted, TierCloud:
		return name != "identity.go" || value != TierPersonal
	}
	return false
}

func TestClaimantFactsGuardRejectsDuplicateRules(t *testing.T) {
	for _, expression := range []string{`record.Origin.Personal`, `"here"`, `"personal"`, `"github-actions"`, `"self-hosted"`, `"cloud"`} {
		node, err := parser.ParseExpr(expression)
		if err != nil {
			t.Fatal(err)
		}
		if !claimantRuleOutsideOwner("duplicate.go", node) {
			t.Fatalf("guard accepted %s", expression)
		}
	}
}
