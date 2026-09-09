package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
)

func surveyRows() []githooks.RepoGates {
	return []githooks.RepoGates{
		{Repo: "/code/xwing", Declared: []string{"pre-commit"}, State: githooks.GateArmed},
		{
			Repo:      "/code/overwing",
			Declared:  []string{"pre-commit"},
			Installed: []string{"pre-commit"},
			Shadowed:  []string{"pre-commit"},
			ActiveDir: "/config/git/hooks",
			Scope:     "global",
			State:     githooks.GateShadowed,
		},
		{
			Repo:     "/code/pulsewing",
			Declared: []string{"pre-commit"},
			Missing:  []string{"pre-commit"},
			State:    githooks.GateUninstalled,
		},
	}
}

func TestRenderHooksSurvey_CountsAndNamesTheUngatedRepos(t *testing.T) {
	var buf bytes.Buffer
	if err := renderHooksSurvey(&buf, surveyRows(), "pretty"); err != nil {
		t.Fatalf("renderHooksSurvey: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "2 of 3 repo(s) do not run a gate of their own") {
		t.Errorf("output = %q, want the ungated count", got)
	}
	for _, want := range []string{"overwing", "pulsewing", "/config/git/hooks", "no gate is installed"} {
		if !strings.Contains(got, want) {
			t.Errorf("output = %q, want it to mention %q", got, want)
		}
	}
}

func TestRenderHooksSurvey_SaysSoWhenEveryGateFires(t *testing.T) {
	rows := []githooks.RepoGates{
		{Repo: "/code/xwing", Declared: []string{"pre-commit"}, State: githooks.GateArmed},
		{Repo: "/code/bitwing", Declared: []string{"pre-commit", "pre-push"}, State: githooks.GateArmed},
	}
	var buf bytes.Buffer
	if err := renderHooksSurvey(&buf, rows, "pretty"); err != nil {
		t.Fatalf("renderHooksSurvey: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "every declared gate fires") {
		t.Errorf("output = %q, want the clean verdict", got)
	}
}

func TestRenderHooksSurvey_JSONRoundTripsEveryRow(t *testing.T) {
	var buf bytes.Buffer
	if err := renderHooksSurvey(&buf, surveyRows(), "json"); err != nil {
		t.Fatalf("renderHooksSurvey: %v", err)
	}
	got := decodeNDJSON[githooks.RepoGates](t, buf.String())
	if len(got) != 3 {
		t.Fatalf("rows = %d, want 3", len(got))
	}
	if got[1].State != githooks.GateShadowed || got[1].Scope != "global" {
		t.Errorf("shadowed row = %+v, want the global-scope redirect preserved", got[1])
	}
}

func TestRenderHooksSurvey_EmptyFleetEncodesAsAnEmptyStream(t *testing.T) {
	var buf bytes.Buffer
	if err := renderHooksSurvey(&buf, nil, "json"); err != nil {
		t.Fatalf("renderHooksSurvey: %v", err)
	}
	if got := buf.String(); got != "" {
		t.Errorf("output = %q, want an empty stream", got)
	}
}

func TestRenderHooksSurvey_PlainIsOneTabSeparatedRowPerRepo(t *testing.T) {
	var buf bytes.Buffer
	if err := renderHooksSurvey(&buf, surveyRows(), "plain"); err != nil {
		t.Fatalf("renderHooksSurvey: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want one per repo:\n%s", len(lines), buf.String())
	}
	if got := lines[0]; got != "/code/xwing\tarmed\t" {
		t.Errorf("armed row = %q, want the repo, state and an empty missing field", got)
	}
	if got := lines[2]; got != "/code/pulsewing\tuninstalled\tpre-commit" {
		t.Errorf("ungated row = %q", got)
	}
}

func TestRenderHooksSurvey_EmptyFleetTellsTheOperatorToRegisterOne(t *testing.T) {
	var buf bytes.Buffer
	if err := renderHooksSurvey(&buf, nil, "pretty"); err != nil {
		t.Fatalf("renderHooksSurvey: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "sparkwing configure xrepo add") {
		t.Errorf("output = %q, want the registration hint", got)
	}
}

func TestRenderHooksSurvey_DoesNotCallARepoArmedWhenNothingCanRefuseACommit(t *testing.T) {
	rows := []githooks.RepoGates{
		{Repo: "/code/toolbox", Declared: []string{"post-commit"}, Firing: []string{"post-commit"}, State: githooks.GateArmed},
	}
	var buf bytes.Buffer
	if err := renderHooksSurvey(&buf, rows, "pretty"); err != nil {
		t.Fatalf("renderHooksSurvey: %v", err)
	}
	row := rowFor(t, buf.String(), "toolbox")
	if strings.Contains(row, "armed") {
		t.Errorf("row = %q, want it not to read armed: a post-commit notifier refuses nothing", row)
	}
	if !strings.Contains(row, "post-commit") {
		t.Errorf("row = %q, want it to name the hook that does fire", row)
	}
}

func TestRenderHooksSurvey_DoesNotClaimEveryGateFiresWhileAHookIsMissing(t *testing.T) {
	rows := []githooks.RepoGates{
		{
			Repo:     "/code/xwing",
			Declared: []string{"post-commit", "pre-commit"},
			Missing:  []string{"post-commit"},
			State:    githooks.GateUninstalled,
		},
	}
	var buf bytes.Buffer
	if err := renderHooksSurvey(&buf, rows, "pretty"); err != nil {
		t.Fatalf("renderHooksSurvey: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "every declared gate fires") {
		t.Errorf("output = %q, want no clean verdict while a declared hook is missing", got)
	}
	if !strings.Contains(got, "post-commit") {
		t.Errorf("output = %q, want the footer to name the hook that does not fire", got)
	}
}

func rowFor(t *testing.T, out, repo string) string {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, repo) {
			return line
		}
	}
	t.Fatalf("no row for %s in:\n%s", repo, out)
	return ""
}
