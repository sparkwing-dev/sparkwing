package repos

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeOps struct {
	dirty      bool
	pin        string
	replace    string
	pipelines  []string
	planBefore map[string]Plan
	planAfter  map[string]Plan
	afterBump  bool
	bumpErr    error
	planErr    error
	beforeErr  map[string]error
	onBump     func()
	verifyErr  error
	commitErr  error
	committed  bool
	restored   bool
	commitMsg  string
}

func (f *fakeOps) Dirty(string) (bool, error)         { return f.dirty, nil }
func (f *fakeOps) Pin(string) (string, string)        { return f.pin, f.replace }
func (f *fakeOps) Pipelines(string) ([]string, error) { return f.pipelines, nil }
func (f *fakeOps) Plan(_, pipeline string) (Plan, error) {
	if f.planErr != nil && f.afterBump {
		return Plan{}, f.planErr
	}
	if f.afterBump {
		return f.planAfter[pipeline], nil
	}
	if err := f.beforeErr[pipeline]; err != nil {
		return Plan{}, err
	}
	return f.planBefore[pipeline], nil
}
func (f *fakeOps) Snapshot(string) ([]byte, error) { return []byte("snap"), nil }
func (f *fakeOps) Restore(string, []byte) error    { f.restored = true; return nil }
func (f *fakeOps) Bump(string, string) error {
	f.afterBump = true
	if f.onBump != nil {
		f.onBump()
	}
	return f.bumpErr
}
func (f *fakeOps) Verify(string) error { return f.verifyErr }
func (f *fakeOps) Commit(_, msg string) error {
	if f.commitErr != nil {
		return f.commitErr
	}
	f.committed = true
	f.commitMsg = msg
	return nil
}

func planWith(ids ...string) Plan {
	p := Plan{Pipeline: "p"}
	for _, id := range ids {
		p.Nodes = append(p.Nodes, PlanNode{ID: id, Decision: "run"})
	}
	return p
}

func TestUpdateRepo_CleanWhenPlanIdentical(t *testing.T) {
	f := &fakeOps{
		pin:        "v0.15.6",
		pipelines:  []string{"p"},
		planBefore: map[string]Plan{"p": planWith("a", "b")},
		planAfter:  map[string]Plan{"p": planWith("a", "b")},
	}
	v := UpdateRepo(f, "/repo", "my-app", UpdateConfig{Target: "v0.15.8"})
	if v.Kind != VerdictClean {
		t.Fatalf("kind = %s, want clean", v.Kind)
	}
	if !f.restored {
		t.Error("dry run should restore the tree")
	}
	if f.committed {
		t.Error("dry run must not commit")
	}
}

func TestUpdateRepo_PlanDiffersRendersDiff(t *testing.T) {
	f := &fakeOps{
		pin:        "v0.15.6",
		pipelines:  []string{"p"},
		planBefore: map[string]Plan{"p": planWith("a", "b")},
		planAfter:  map[string]Plan{"p": planWith("a", "b", "c")},
	}
	v := UpdateRepo(f, "/repo", "my-app", UpdateConfig{Target: "v0.15.8"})
	if v.Kind != VerdictPlanDiffers {
		t.Fatalf("kind = %s, want plan-differs", v.Kind)
	}
	if len(v.Diffs) != 1 || v.Diffs[0].Diff.Identical {
		t.Fatalf("expected a non-identical diff, got %+v", v.Diffs)
	}
}

func TestUpdateRepo_BrokenOnCompileCarriesGuides(t *testing.T) {
	f := &fakeOps{
		pin:        "v0.15.6",
		pipelines:  []string{"p"},
		planBefore: map[string]Plan{"p": planWith("a")},
		planErr:    errors.New("undefined: sparkwing.OldAPI"),
	}
	cfg := UpdateConfig{
		Target: "v0.15.8",
		GuidesFor: func(from, to string) []Guide {
			return []Guide{{Version: "v0.15.7", Title: "Renamed OldAPI"}}
		},
	}
	v := UpdateRepo(f, "/repo", "my-app", cfg)
	if v.Kind != VerdictBroken {
		t.Fatalf("kind = %s, want broken", v.Kind)
	}
	if v.Err == "" || !f.restored {
		t.Error("broken bump should carry an error and restore the tree")
	}
	if len(v.Guides) != 1 || v.Guides[0].Version != "v0.15.7" {
		t.Errorf("broken verdict should attach crossed guides, got %+v", v.Guides)
	}
}

func TestUpdateRepo_ApplyCommitsWithConventionalMessage(t *testing.T) {
	f := &fakeOps{
		pin:        "v0.15.6",
		pipelines:  []string{"p"},
		planBefore: map[string]Plan{"p": planWith("a")},
		planAfter:  map[string]Plan{"p": planWith("a")},
	}
	v := UpdateRepo(f, "/repo", "my-app", UpdateConfig{Target: "v0.15.8", Apply: true})
	if v.Kind != VerdictClean || !v.Committed {
		t.Fatalf("apply of a clean bump should commit; got %s committed=%v", v.Kind, v.Committed)
	}
	if f.commitMsg != "chore: bump sparkwing SDK to v0.15.8" {
		t.Errorf("commit message = %q", f.commitMsg)
	}
	if f.restored {
		t.Error("apply must not restore")
	}
}

func TestUpdateRepo_SkipsDirty(t *testing.T) {
	f := &fakeOps{pin: "v0.15.6", dirty: true}
	v := UpdateRepo(f, "/repo", "my-app", UpdateConfig{Target: "v0.15.8"})
	if v.Kind != VerdictSkippedDirty {
		t.Fatalf("kind = %s, want skipped-dirty", v.Kind)
	}
	if f.afterBump {
		t.Error("dirty repo must not be bumped")
	}
}

func TestUpdateRepo_UpToDate(t *testing.T) {
	f := &fakeOps{pin: "v0.15.8"}
	v := UpdateRepo(f, "/repo", "my-app", UpdateConfig{Target: "v0.15.8"})
	if v.Kind != VerdictUpToDate {
		t.Fatalf("kind = %s, want up-to-date", v.Kind)
	}
}

func TestUpdateRepo_SkipsReplaceDirective(t *testing.T) {
	f := &fakeOps{pin: "v0.15.6", replace: "../sparkwing"}
	v := UpdateRepo(f, "/repo", "my-app", UpdateConfig{Target: "v0.15.8"})
	if v.Kind != VerdictSkippedMissing {
		t.Fatalf("kind = %s, want skipped-missing", v.Kind)
	}
}

func TestUpdateRepo_VerifyFailureIsBroken(t *testing.T) {
	f := &fakeOps{
		pin:        "v0.15.6",
		pipelines:  []string{"p"},
		planBefore: map[string]Plan{"p": planWith("a")},
		planAfter:  map[string]Plan{"p": planWith("a")},
		verifyErr:  errors.New("pre-commit: lint failed"),
	}
	v := UpdateRepo(f, "/repo", "my-app", UpdateConfig{Target: "v0.15.8", Verify: true})
	if v.Kind != VerdictBroken {
		t.Fatalf("kind = %s, want broken", v.Kind)
	}
	if !f.restored {
		t.Error("verify failure should restore")
	}
}

func TestUpdateRepo_ReportsEachStep(t *testing.T) {
	f := &fakeOps{
		pin:        "v0.15.6",
		pipelines:  []string{"a", "b"},
		planBefore: map[string]Plan{"a": planWith("x"), "b": planWith("y")},
		planAfter:  map[string]Plan{"a": planWith("x"), "b": planWith("y")},
	}
	var got []string
	cfg := UpdateConfig{Target: "v0.15.8", Verify: true, Apply: true, Progress: func(repo, msg string) {
		got = append(got, repo+": "+msg)
	}}
	if v := UpdateRepo(f, "/repo", "my-app", cfg); v.Kind != VerdictClean {
		t.Fatalf("kind = %s, want clean", v.Kind)
	}
	want := []string{
		"my-app: listing pipelines",
		"my-app: plan 1/2 a (before bump)",
		"my-app: plan 2/2 b (before bump)",
		"my-app: bumping v0.15.6 -> v0.15.8 and tidying modules",
		"my-app: compiling pipelines after bump",
		"my-app: plan 1/2 a (after bump)",
		"my-app: plan 2/2 b (after bump)",
		"my-app: running pre-commit gate",
		"my-app: committing",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("progress =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestUpdateFleet_NumbersReposAndReportsVerdicts(t *testing.T) {
	f := &fakeOps{pin: "v0.15.8"}
	fleet := []Repo{{Name: "ghost"}, {Name: "same", Primary: "/same"}}
	var got []string
	cfg := UpdateConfig{Target: "v0.15.8", Progress: func(repo, msg string) {
		got = append(got, repo+": "+msg)
	}}
	UpdateFleet(f, fleet, cfg)
	want := []string{
		"[1/2] ghost: skipped-missing: observed in runs but no local checkout registered",
		"[2/2] same: up-to-date",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("progress =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestUpdateFleet_SilentWithoutProgress(t *testing.T) {
	f := &fakeOps{pin: "v0.15.8"}
	vs := UpdateFleet(f, []Repo{{Name: "same", Primary: "/same"}}, UpdateConfig{Target: "v0.15.8"})
	if len(vs) != 1 || vs[0].Kind != VerdictUpToDate {
		t.Fatalf("verdicts = %+v", vs)
	}
}

func TestUpdateRepo_BaselineUnplannableIsSetAside(t *testing.T) {
	f := &fakeOps{
		pin:        "v0.15.6",
		pipelines:  []string{"deploy", "gate"},
		beforeErr:  map[string]error{"deploy": errors.New("inputs for pipeline \"deploy\": --tag is required")},
		planBefore: map[string]Plan{"gate": planWith("a")},
		planAfter:  map[string]Plan{"gate": planWith("a")},
	}
	v := UpdateRepo(f, "/repo", "my-app", UpdateConfig{Target: "v0.15.8"})
	if v.Kind != VerdictClean {
		t.Fatalf("kind = %s (%s), want clean", v.Kind, v.Err)
	}
	if len(v.Diffs) != 1 || v.Diffs[0].Pipeline != "gate" {
		t.Fatalf("diffs = %+v, want only gate", v.Diffs)
	}
	if len(v.Uncompared) != 1 || v.Uncompared[0].Pipeline != "deploy" || !strings.Contains(v.Uncompared[0].Reason, "--tag") {
		t.Fatalf("uncompared = %+v", v.Uncompared)
	}
	if !strings.Contains(verdictLine(v), "1 pipeline(s) not compared") {
		t.Errorf("verdict line = %q", verdictLine(v))
	}
}

func TestUpdateRepo_InterruptMidBumpRestoresInsteadOfBroken(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeOps{
		pin:        "v0.15.6",
		pipelines:  []string{"p"},
		planBefore: map[string]Plan{"p": planWith("a")},
		planErr:    errors.New("signal: interrupt"),
	}
	f.onBump = cancel
	v := UpdateRepo(f, "/repo", "my-app", UpdateConfig{Target: "v0.15.8", Context: ctx})
	if v.Kind != VerdictInterrupted {
		t.Fatalf("kind = %s (%s), want interrupted", v.Kind, v.Err)
	}
	if !f.restored {
		t.Error("interrupt must restore the module files")
	}
	if v.Err != "" {
		t.Errorf("an interrupt is not a broken bump: %q", v.Err)
	}
}

func TestUpdateFleet_StopsWalkingAfterInterrupt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeOps{
		pin:        "v0.15.6",
		pipelines:  []string{"p"},
		planBefore: map[string]Plan{"p": planWith("a")},
		planAfter:  map[string]Plan{"p": planWith("a")},
	}
	f.onBump = cancel
	fleet := []Repo{{Name: "first", Primary: "/first"}, {Name: "second", Primary: "/second"}}
	vs := UpdateFleet(f, fleet, UpdateConfig{Target: "v0.15.8", Context: ctx})
	if len(vs) != 1 || vs[0].Kind != VerdictInterrupted {
		t.Fatalf("verdicts = %+v, want one interrupted verdict", vs)
	}
}

type restoreFailOps struct{ fakeOps }

func (r *restoreFailOps) Restore(string, []byte) error { return errors.New("disk full") }

func TestUpdateRepo_RestoreFailureIsNamed(t *testing.T) {
	f := &restoreFailOps{fakeOps{
		pin:        "v0.15.6",
		pipelines:  []string{"p"},
		planBefore: map[string]Plan{"p": planWith("a")},
		planAfter:  map[string]Plan{"p": planWith("a")},
	}}
	v := UpdateRepo(f, "/repo", "my-app", UpdateConfig{Target: "v0.15.8"})
	if !strings.Contains(v.Detail, "restore failed") || !strings.Contains(v.Detail, "disk full") {
		t.Fatalf("detail = %q, want the restore failure named", v.Detail)
	}
}
