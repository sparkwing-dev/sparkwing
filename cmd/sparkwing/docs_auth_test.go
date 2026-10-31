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
	for _, grant := range capability.Grants() {
		fmt.Fprintf(&expected, "| `%s` |\n", grant)
	}
	if rows != expected.String() {
		t.Fatalf("update docs/auth.md grant reference from capability.Grants(), then sync embedded docs:\n%s", expected.String())
	}
}

func TestAuthGuideCLIExamplesUseFinalGrammar(t *testing.T) {
	commands := 0
	for _, topic := range []string{"credentials", "auth"} {
		body, err := docs.ReadRaw(topic)
		if err != nil {
			t.Fatal(err)
		}
		inFence := false
		var examples strings.Builder
		for _, line := range strings.Split(strings.ReplaceAll(body, "\\\n", ""), "\n") {
			if strings.HasPrefix(line, "```") {
				inFence = !inFence
				examples.WriteString(line + "\n")
				continue
			}
			if inFence {
				examples.WriteString(line + "\n")
			}
		}
		for _, unit := range codeUnits(examples.String()) {
			if !strings.HasPrefix(unit, "sparkwing ") {
				continue
			}
			args := strings.Fields(unit)
			if _, err := clisurface.Parse("sparkwing", args[1:]); err != nil {
				t.Errorf("auth example %q: %v", unit, err)
			}
			commands++
		}
	}
	if commands == 0 {
		t.Fatal("auth guide contains no CLI examples")
	}
}

func TestAuthGuideActionsExpiryUsesStoreClock(t *testing.T) {
	body, err := docs.ReadRaw("auth")
	if err != nil {
		t.Fatal(err)
	}
	for _, clause := range []string{"one hour from the store transaction clock", "one hour after the verified GitHub token expires"} {
		if !strings.Contains(body, clause) {
			t.Errorf("auth guide is missing the Actions expiry bound %q", clause)
		}
	}
}

func TestCloudCapacityGuidesMatchFailureContract(t *testing.T) {
	for _, topic := range []string{"auth", "deployment", "cli-runs"} {
		body, err := docs.ReadRaw(topic)
		if err != nil {
			t.Fatal(err)
		}
		body = strings.Join(strings.Fields(body), " ")
		for _, text := range []string{capability.CloudCapacityUnconfigured, "support@sparkwing.dev", "Waiting for dedicated Cloud capacity", "elapsed"} {
			if !strings.Contains(body, text) {
				t.Errorf("%s omits %q", topic, text)
			}
		}
	}
}
