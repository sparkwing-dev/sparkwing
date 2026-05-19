package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Every "unknown pipeline X" surface in the orchestrator
// (printPipelineHelp, printPipelinePlan, parseTypedFlags,
// printPipelineRuntimePlan) routes through unknownPipelineErr,
// which composes a Levenshtein "did you mean Y?" suggestion when
// the typo is close to a registered name. These tests pin the
// suggestion behavior so a future helper edit can't silently
// regress to the flat error.

// suggestFixturePipe is a minimal registered pipeline used to
// populate sparkwing.Registered() for the suggestion tests.
type suggestFixturePipe struct{ sparkwing.Base }

func (suggestFixturePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	return nil
}

func registerSuggestFixtures(t *testing.T) {
	t.Helper()
	// Names chosen so "claster-up" is one edit from "cluster-up"
	// (close), "totallyunrelated" is far from any (no suggestion),
	// and "helo" / "hello" exercises the typo-suggestion path.
	for _, n := range []string{"cluster-up", "cluster-down", "hello"} {
		// Re-register is idempotent if the same name was used in a
		// previous test run; sparkwing.Register panics on duplicate
		// so we guard via Lookup.
		if _, ok := sparkwing.Lookup(n); ok {
			continue
		}
		sparkwing.Register[sparkwing.NoInputs](n,
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return suggestFixturePipe{} })
	}
}

func TestUnknownPipelineErr_SuggestsCloseMatch(t *testing.T) {
	registerSuggestFixtures(t)
	err := unknownPipelineErr("claster-up")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		`unknown pipeline "claster-up"`,
		`did you mean "cluster-up"`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q\nfull: %s", want, msg)
		}
	}
}

func TestUnknownPipelineErr_TypoOfHello(t *testing.T) {
	registerSuggestFixtures(t)
	err := unknownPipelineErr("helo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, `did you mean "hello"`) {
		t.Errorf("expected suggestion of 'hello' for typo 'helo', got: %s", msg)
	}
}

func TestUnknownPipelineErr_FarTypoNoSuggestion(t *testing.T) {
	registerSuggestFixtures(t)
	err := unknownPipelineErr("totallyunrelated")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, `unknown pipeline "totallyunrelated"`) {
		t.Errorf("expected base message, got: %s", msg)
	}
	if strings.Contains(msg, "did you mean") {
		t.Errorf("far typo must not produce a misleading suggestion, got: %s", msg)
	}
}

// parseTypedFlags is one of the four orchestrator call sites;
// verify it routes through unknownPipelineErr.
func TestParseTypedFlags_UnknownPipelineSuggests(t *testing.T) {
	registerSuggestFixtures(t)
	_, err := parseTypedFlags("claster-up", nil)
	if err == nil {
		t.Fatal("expected error for typo'd pipeline")
	}
	if !strings.Contains(err.Error(), `did you mean "cluster-up"`) {
		t.Errorf("parseTypedFlags should surface unknown-pipeline suggestion, got: %s", err)
	}
}

// printPipelineHelp is the second call site. The function writes to
// stdout on success; on the unknown-pipeline path it returns an
// error before any output.
func TestPrintPipelineHelp_UnknownPipelineSuggests(t *testing.T) {
	registerSuggestFixtures(t)
	err := printPipelineHelp("claster-up")
	if err == nil {
		t.Fatal("expected error for typo'd pipeline")
	}
	if !strings.Contains(err.Error(), `did you mean "cluster-up"`) {
		t.Errorf("printPipelineHelp should surface unknown-pipeline suggestion, got: %s", err)
	}
}

// printPipelinePlan (--explain) is the third call site.
func TestPrintPipelinePlan_UnknownPipelineSuggests(t *testing.T) {
	registerSuggestFixtures(t)
	err := printPipelinePlan("claster-up", nil)
	if err == nil {
		t.Fatal("expected error for typo'd pipeline")
	}
	if !strings.Contains(err.Error(), `did you mean "cluster-up"`) {
		t.Errorf("printPipelinePlan should surface unknown-pipeline suggestion, got: %s", err)
	}
}

// printPipelineRuntimePlan (--plan) is the fourth call site. Lives
// in printpipelineplan.go.
func TestPrintPipelineRuntimePlan_UnknownPipelineSuggests(t *testing.T) {
	registerSuggestFixtures(t)
	err := printPipelineRuntimePlan("claster-up", nil)
	if err == nil {
		t.Fatal("expected error for typo'd pipeline")
	}
	if !strings.Contains(err.Error(), `did you mean "cluster-up"`) {
		t.Errorf("printPipelineRuntimePlan should surface unknown-pipeline suggestion, got: %s", err)
	}
}
