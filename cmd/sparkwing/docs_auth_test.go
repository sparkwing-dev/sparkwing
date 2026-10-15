package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func TestAuthGuideGrantVocabularyMatchesCredentialContract(t *testing.T) {
	body, err := docs.ReadRaw("auth")
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(body, "<!-- credential-grants:begin -->\n")
	if !ok {
		t.Fatal("auth guide has no generated grant reference")
	}
	rows, _, ok := strings.Cut(after, "<!-- credential-grants:end -->")
	if !ok {
		t.Fatal("auth grant reference is unclosed")
	}
	var expected strings.Builder
	expected.WriteString("| Grant |\n| --- |\n")
	for _, grant := range capability.Grants {
		fmt.Fprintf(&expected, "| `%s` |\n", grant)
	}
	if rows != expected.String() {
		t.Fatalf("update docs/auth.md grant reference from capability.Grants, then sync embedded docs:\n%s", expected.String())
	}
}

func TestAuthGuideCLIExamplesUseFinalGrammar(t *testing.T) {
	body, err := docs.ReadRaw("credentials")
	if err != nil {
		t.Fatal(err)
	}
	commands := 0
	for _, unit := range codeUnits(strings.ReplaceAll(body, "\\\n", "")) {
		if !strings.HasPrefix(unit, "sparkwing ") {
			continue
		}
		args := strings.Fields(unit)
		if _, err := clisurface.Parse("sparkwing", args[1:]); err != nil {
			t.Errorf("auth example %q: %v", unit, err)
		}
		commands++
	}
	if commands == 0 {
		t.Fatal("auth guide contains no CLI examples")
	}
}
