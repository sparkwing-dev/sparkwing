package main

import (
	"go/ast"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func TestHelpFlagsMatchFinalGrammar(t *testing.T) {
	for _, command := range clisurface.Registry() {
		if command.Path != "sparkwing" && !strings.HasPrefix(command.Path, "sparkwing ") {
			continue
		}
		shown := surfaceHelp(command)
		flags := append(clisurface.GlobalFlags(), command.Flags...)
		if len(shown.Flags) != len(flags) {
			t.Errorf("%s: help has %d flags, grammar has %d", command.Path, len(shown.Flags), len(flags))
			continue
		}
		for i, flag := range flags {
			got := shown.Flags[i]
			if got.Name != flag.Name || got.Short != flag.Short || got.Argument != flag.Argument || got.Required != flag.Required ||
				!slices.Equal(got.RequiresFlags, flag.Requires) || !slices.Equal(got.ConflictsWith, flag.Conflicts) {
				t.Errorf("%s --%s: help grammar differs: %+v", command.Path, flag.Name, got)
			}
		}
	}
}

func TestHelpUsesFinalSharedFlagMeanings(t *testing.T) {
	for _, command := range allCommands {
		for _, flag := range command.Flags {
			if flag.Name == "limit" && !strings.Contains(flag.Desc, "at least one") {
				t.Errorf("%s advertises an invalid pagination limit: %q", command.Path, flag.Desc)
			}
			if flag.Name == "profile" && strings.Contains(strings.ToLower(flag.Desc), "current default") {
				t.Errorf("%s advertises an implicit profile: %q", command.Path, flag.Desc)
			}
		}
	}
}

func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}
