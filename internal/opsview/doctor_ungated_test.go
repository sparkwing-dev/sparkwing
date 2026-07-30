package opsview_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
	"github.com/sparkwing-dev/sparkwing/internal/opsview"
)

func ungatedReport() opsview.DoctorReport {
	return opsview.DoctorReport{
		GatesSurveyed: 1,
		UngatedRepos: []githooks.RepoGates{
			{
				Repo:      "/code/pulsewing",
				Declared:  []string{"pre-commit"},
				Installed: []string{"pre-commit"},
				Shadowed:  []string{"pre-commit"},
				ActiveDir: "/config/git/hooks",
				Scope:     "global",
				State:     githooks.GateShadowed,
			},
		},
	}
}

// Which repos git gates is machine configuration, not this home's state, so
// it must not decide a home's verdict the way an orphaned run does.
func TestDoctorReport_UngatedReposDoNotDecideCleanliness(t *testing.T) {
	if !ungatedReport().Clean() {
		t.Fatal("a home with nothing to repair reported unclean because another checkout is ungated")
	}
}

func TestRenderDoctorPretty_NamesEachUngatedRepoAndItsFix(t *testing.T) {
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, ungatedReport(), "", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"/code/pulsewing", "/config/git/hooks", "sparkwing pipeline hooks install --repo /code/pulsewing"} {
		if !strings.Contains(out, want) {
			t.Errorf("pretty output does not carry %q:\n%s", want, out)
		}
	}
}

// A doctor run with nothing to repair is exactly where an ungated repo would
// otherwise go unmentioned.
func TestRenderDoctorPretty_ReportsUngatedReposOnTheHealthyPath(t *testing.T) {
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, ungatedReport(), "", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "healthy") {
		t.Fatalf("expected the healthy path for this report:\n%s", out)
	}
	if !strings.Contains(out, "accept commits with no gate") {
		t.Errorf("healthy output dropped the ungated warning:\n%s", out)
	}
}

// The checkout doctor was run from is already described in full under
// shadowed_hooks; repeating it under the fleet heading reads as two findings.
func TestRenderDoctorPretty_DoesNotRepeatTheCheckoutShadowedHooksDescribes(t *testing.T) {
	r := ungatedReport()
	r.ShadowedHooks = &githooks.Shadow{
		Repo:      "/code/pulsewing",
		HooksDir:  "/code/pulsewing/.git/hooks",
		ActiveDir: "/config/git/hooks",
		Scope:     "global",
		Gates:     []string{"pre-commit"},
	}
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, r, "", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := strings.Count(buf.String(), "/code/pulsewing/.git/hooks"); got != 1 {
		t.Errorf("hooks dir named %d times, want 1:\n%s", got, buf.String())
	}
	if strings.Contains(buf.String(), "1 registered repo(s) accept commits with no gate") {
		t.Errorf("fleet heading rendered for the one repo already described:\n%s", buf.String())
	}
}

func TestRenderDoctorPretty_StillNamesOtherReposWhenTheLocalOneIsShadowed(t *testing.T) {
	r := ungatedReport()
	r.UngatedRepos = append(r.UngatedRepos, githooks.RepoGates{
		Repo:     "/code/sparks-core",
		Declared: []string{"pre-commit"},
		Missing:  []string{"pre-commit"},
		State:    githooks.GateUninstalled,
	})
	r.ShadowedHooks = &githooks.Shadow{Repo: "/code/pulsewing", Gates: []string{"pre-commit"}}
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, r, "", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), "/code/sparks-core") {
		t.Errorf("pretty output dropped the other ungated repo:\n%s", buf.String())
	}
}

func TestRenderDoctorPretty_SaysNothingAboutGatesWhenEveryRepoIsArmed(t *testing.T) {
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, opsview.DoctorReport{GatesSurveyed: 3}, "", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(buf.String(), "no gate") {
		t.Errorf("clean fleet still produced a gate warning:\n%s", buf.String())
	}
}

// A surveyed fleet with nothing ungated has to say so. Silence would be
// identical to the output of a build that never ran the survey, and a reader
// looking for problems finds none either way -- the false all-clear.
func TestRenderDoctorPretty_StatesTheCleanGateVerdictRatherThanImplyingIt(t *testing.T) {
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, opsview.DoctorReport{GatesSurveyed: 3}, "", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "3 registered repo(s) surveyed, every declared gate fires") {
		t.Errorf("clean gate verdict not stated:\n%s", got)
	}
}

// The negative control for the line above: a report that never surveyed the
// fleet must not claim a clean one.
func TestRenderDoctorPretty_ClaimsNoGateVerdictWhenNothingWasSurveyed(t *testing.T) {
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, opsview.DoctorReport{}, "", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := buf.String(); strings.Contains(got, "every declared gate fires") {
		t.Errorf("an unsurveyed report claimed a gated fleet:\n%s", got)
	}
}

// The count is the field that says the question was asked, so it has to be in
// the JSON whatever its value -- an omitted zero reads as a build that cannot
// survey, which is the distinction the field exists to draw.
func TestRenderDoctorJSON_AlwaysCarriesTheSurveyedGateCount(t *testing.T) {
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, opsview.DoctorReport{}, "json", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), `"gates_surveyed"`) {
		t.Errorf("json dropped gates_surveyed when it was zero:\n%s", buf.String())
	}
}

func TestRenderDoctorJSON_CarriesUngatedRepos(t *testing.T) {
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, ungatedReport(), "json", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	var got opsview.DoctorReport
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.UngatedRepos) != 1 || got.UngatedRepos[0].State != githooks.GateShadowed {
		t.Errorf("round-tripped ungated repos = %+v, want the shadowed pulsewing row", got.UngatedRepos)
	}
}

func TestRenderDoctorPlain_CountsUngatedRepos(t *testing.T) {
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, ungatedReport(), "plain", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), "ungated_repos") {
		t.Errorf("plain output does not carry the ungated count:\n%s", buf.String())
	}
}
