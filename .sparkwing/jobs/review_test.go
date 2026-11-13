package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testPolicy(t *testing.T) reviewPolicy {
	t.Helper()
	p, err := parseReviewPolicy([]byte(`version: 1
objective:
  sources: [brief, ticket, commit-message]
rules:
  - id: standards
    from: overwing-standards
  - id: tenant-isolation
    text: One team's data is never visible to another team.
strict:
  - name: billing
    paths: [internal/**/*billing*]
    block_at: P2
    reviewer: security
never_block: [style]
reviewers:
  default: { model: fake-default }
  security: { model: fake-security }
rereview:
  same_reviewer: true
  skip_allowed_from: P2
`))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRepositoryReviewPolicyParses(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(sourceTreeRoot(), ".review.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := parseReviewPolicy(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{p.Reviewers["default"].Model, p.Reviewers["security"].Model} {
		if _, err := newReviewer(model); err != nil {
			t.Errorf("model %q: %v", model, err)
		}
	}
	for _, file := range []string{
		"internal/controller/billing_routes.go",
		"internal/controller/tenant_scope.go",
		"internal/source/credential.go",
		"internal/secrets/mask.go",
	} {
		if got := p.strictReviewers([]string{file}); len(got["security"]) != 1 {
			t.Errorf("%s is not a strict path for the security reviewer", file)
		}
	}
	if got := p.strictReviewers([]string{"internal/controller/runs.go"}); len(got) != 0 {
		t.Errorf("an ordinary file is strict: %v", got)
	}
	if _, err := newReviewer(p.Reviewers[p.DocsOnly.Reviewer].Model); err != nil {
		t.Errorf("docs_only reviewer: %v", err)
	}
	docs := []string{"docs/admission.md", "pkg/docs/changelog.md", "CHANGELOG.md", "README.md", "design/rewrite/ORIENTATION.md"}
	if got := p.defaultModel(docs); got != "claude-haiku" {
		t.Errorf("docs-only diff routes to %q, want claude-haiku", got)
	}
	for _, mixed := range [][]string{
		append([]string{"pkg/docs/docs.go"}, docs...),
		{"docs/admission.md", ".review.yaml"},
		{},
	} {
		if want := p.Reviewers[defaultReviewer].Model; p.defaultModel(mixed) != want {
			t.Errorf("%v routes to %q, want the default reviewer %q", mixed, p.defaultModel(mixed), want)
		}
	}
}

func TestReviewPolicyRejectsLooserOrMistypedSettings(t *testing.T) {
	for name, body := range map[string]string{
		"unknown key":      "version: 1\nreviewers: {default: {model: m}}\nstrcit: []\n",
		"skip P1":          "version: 1\nreviewers: {default: {model: m}}\nrereview: {skip_allowed_from: P1}\n",
		"bad block_at":     "version: 1\nreviewers: {default: {model: m}}\nstrict: [{paths: [a], block_at: P9}]\n",
		"unknown reviewer": "version: 1\nreviewers: {default: {model: m}}\nstrict: [{paths: [a], block_at: P2, reviewer: sec}]\n",
		"no default":       "version: 1\nreviewers: {security: {model: m}}\n",
		"bad source":       "version: 1\nreviewers: {default: {model: m}}\nobjective: {sources: [jira]}\n",
	} {
		if _, err := parseReviewPolicy([]byte(body)); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
}

func TestGlobMatch(t *testing.T) {
	for _, c := range []struct {
		pat, name string
		want      bool
	}{
		{"internal/**/*billing*", "internal/controller/billing_routes.go", true},
		{"internal/**/*billing*", "internal/a/b/card_billing.go", true},
		{"internal/**/*billing*", "cmd/sparkwing/billing.go", false},
		{"internal/secrets/**", "internal/secrets/mask.go", true},
		{"internal/secrets/**", "internal/secretsx/mask.go", false},
		{"internal/store/team_*", "internal/store/team_members.go", true},
		{"internal/store/team_*", "internal/store/sub/team_members.go", false},
	} {
		if got := globMatch(c.pat, c.name); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pat, c.name, got, c.want)
		}
	}
}

func TestParseReviewReply(t *testing.T) {
	got, err := parseReviewReply("Here is my review.\n```json\n{\"findings\": [{\"id\": \"F1\", \"severity\": \"p1\", \"rule\": \"tenant-isolation\", \"file\": \"a.go:3\", \"summary\": \"leak\"}]}\n```\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Severity != "P1" || got[0].Status != findingOpen || got[0].File != "a.go:3" {
		t.Fatalf("parsed %+v", got)
	}
	if got, err := parseReviewReply(`{"findings": []}`); err != nil || len(got) != 0 {
		t.Fatalf("empty findings: %v %v", got, err)
	}
	for _, bad := range []string{
		"no json here",
		`{"verdict": "ok"}`,
		`{"findings": [{"id": "F1", "severity": "high", "summary": "x"}]}`,
		`{"findings": [{"id": "F1", "severity": "P2"}]}`,
	} {
		if _, err := parseReviewReply(bad); err == nil {
			t.Errorf("%q parsed without error", bad)
		}
	}
}

func TestVerdictBlocksOnP0P1AndStrictP2(t *testing.T) {
	p := testPolicy(t)
	for _, c := range []struct {
		name string
		f    reviewFinding
		want string
	}{
		{"P0 anywhere", reviewFinding{Severity: "P0", File: "a.go:1", Status: findingOpen}, resultBlock},
		{"P1 anywhere", reviewFinding{Severity: "P1", File: "a.go:1", Status: findingOpen}, resultBlock},
		{"P2 off strict", reviewFinding{Severity: "P2", File: "a.go:1", Status: findingOpen}, resultPass},
		{"P2 on strict", reviewFinding{Severity: "P2", File: "internal/store/billing_policy.go:9", Status: findingOpen}, resultBlock},
		{"P3 on strict", reviewFinding{Severity: "P3", File: "internal/store/billing_policy.go:9", Status: findingOpen}, resultPass},
		{"fixed P0", reviewFinding{Severity: "P0", File: "a.go:1", Status: findingFixed}, resultPass},
		{"skipped P2", reviewFinding{Severity: "P2", File: "a.go:1", Status: findingSkipped}, resultPass},
	} {
		rec := reviewRecord{Findings: []reviewFinding{c.f}}
		rec.settle(p, []string{"a.go", "internal/store/billing_policy.go"})
		if rec.Result != c.want {
			t.Errorf("%s: result %s, want %s", c.name, rec.Result, c.want)
		}
	}
}

func TestSkipRules(t *testing.T) {
	p := testPolicy(t)
	findings := []reviewFinding{
		{ID: "F1", Severity: "P1", File: "a.go:1", Status: findingOpen},
		{ID: "F2", Severity: "P2", File: "a.go:2", Status: findingOpen},
		{ID: "F3", Severity: "P3", File: "a.go:3", Status: findingOpen},
		{ID: "S1", Severity: "P2", File: "internal/store/billing_policy.go:4", Status: findingOpen},
		{ID: "F4", Severity: "P1", File: "a.go:5", Status: findingSkipped, SkipReason: "was P2"},
	}
	skips, err := parseSkips("F1=not now; F2=covered elsewhere; F3 = cosmetic; S1=fine; F9=typo")
	if err != nil {
		t.Fatal(err)
	}
	notes := applySkips(p, findings, skips, []string{"a.go", "internal/store/billing_policy.go"})
	want := map[string]string{"F1": findingOpen, "F2": findingSkipped, "F3": findingSkipped, "S1": findingOpen, "F4": findingOpen}
	for _, f := range findings {
		if f.Status != want[f.ID] {
			t.Errorf("%s: status %s, want %s", f.ID, f.Status, want[f.ID])
		}
	}
	if findings[1].SkipReason != "covered elsewhere" || findings[2].SkipReason != "cosmetic" {
		t.Errorf("skip reasons not recorded: %+v", findings)
	}
	joined := strings.Join(notes, "\n")
	for _, id := range []string{"F1", "S1", "F4", "F9"} {
		if !strings.Contains(joined, id) {
			t.Errorf("no note for %s in %q", id, joined)
		}
	}
	for _, bad := range []string{"F1", "F1=", "=reason"} {
		if _, err := parseSkips(bad); err == nil {
			t.Errorf("parseSkips(%q) accepted", bad)
		}
	}
}

func TestMergeFindingsKeepsSilentFindingsAndRenumbers(t *testing.T) {
	prior := []reviewFinding{
		{ID: "F1", Severity: "P1", Summary: "a", Status: findingOpen, Reviewer: "default"},
		{ID: "F2", Severity: "P2", Summary: "b", Status: findingSkipped, SkipReason: "later", Reviewer: "default"},
		{ID: "S1", Severity: "P1", Summary: "c", Status: findingOpen, Reviewer: "security"},
	}
	reply := []reviewFinding{
		{ID: "F2", Severity: "P2", Summary: "b", Status: findingOpen},
		{ID: "S1", Severity: "P3", Summary: "stolen id", Status: findingOpen},
		{ID: "", Severity: "P3", Summary: "new", Status: findingOpen},
	}
	got := mergeFindings(prior, reply, "default", "F")
	if len(got) != 5 {
		t.Fatalf("merged %d findings: %+v", len(got), got)
	}
	if got[0].Status != findingOpen || got[0].ID != "F1" {
		t.Errorf("a finding the reviewer left out changed: %+v", got[0])
	}
	if got[1].Status != findingSkipped || got[1].SkipReason != "later" {
		t.Errorf("an author skip was dropped: %+v", got[1])
	}
	if got[2].Reviewer != "security" || got[2].Summary != "c" {
		t.Errorf("another profile's finding changed: %+v", got[2])
	}
	if got[3].ID != "F3" || got[4].ID != "F4" {
		t.Errorf("new findings got ids %s and %s, want F3 and F4", got[3].ID, got[4].ID)
	}
}

func TestTicketFromBranchThenTrailer(t *testing.T) {
	for _, c := range []struct{ branch, messages, subjects, want string }{
		{"bw" + "-3095-review-job", "Tickets: BW" + "-7", "feat: x (BW" + "-8)", "BW" + "-3095"},
		{"BW" + "-12", "", "", "BW" + "-12"},
		{"gh-41-fix", "", "", "GH-41"},
		{"feature/x", "feat: x (BW" + "-8)\n\nTickets: BW" + "-7\n", "feat: x (BW" + "-8)", "BW" + "-7"},
		{"feature/x", "feat: x (BW" + "-7)\n", "feat: x (BW" + "-7)", "BW" + "-7"},
		{"feature/x", "fix: y (gh-5)\n", "fix: y (gh-5)", "GH-5"},
		{"feature/x", "feat: x\n\nsee (BW" + "-9) in the body\n", "feat: x", ""},
		{"bw3095-x", "", "", ""},
	} {
		if got := ticketFrom(c.branch, c.messages, c.subjects); got != c.want {
			t.Errorf("ticketFrom(%q, %q, %q) = %q, want %q", c.branch, c.messages, c.subjects, got, c.want)
		}
	}
}

type fakeGit struct {
	head, tree, base string
	files            []string
	messages         string
}

func (g *fakeGit) run(_ context.Context, args ...string) (string, error) {
	switch strings.Join(args[:2], " ") {
	case "rev-parse HEAD":
		return g.head, nil
	case "rev-parse HEAD^{tree}":
		return g.tree, nil
	case "merge-base origin/main":
		return g.base, nil
	case "log --format=%B%n":
		return g.messages, nil
	case "log --format=%s":
		subject, _, _ := strings.Cut(g.messages, "\n")
		return subject, nil
	case "diff --name-status":
		if len(g.files) == 0 {
			return "", nil
		}
		return "M\x00" + strings.Join(g.files, "\x00M\x00") + "\x00", nil
	case "diff --raw":
		var records strings.Builder
		for _, path := range g.files {
			fmt.Fprintf(&records, ":100644 100644 before after M\x00%s\x00", path)
		}
		return records.String(), nil
	case "cat-file blob":
		return "fixture text\n", nil
	case "diff --no-color":
		var patch strings.Builder
		for _, path := range g.files {
			fmt.Fprintf(&patch, "diff --git %q %q\n@@ -0,0 +1,2 @@\n+diff of everything at %s\n+diff of %s\n", "a/"+path, "b/"+path, g.head, path)
		}
		return patch.String(), nil
	}
	return "", errors.New("unexpected git " + strings.Join(args, " "))
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

type fakeCall struct{ session, prompt string }

type fakeReviewer struct {
	replies []string
	calls   []fakeCall
	failOn  string
}

func (f *fakeReviewer) review(_ context.Context, _, session, prompt string) (reviewAnswer, error) {
	f.calls = append(f.calls, fakeCall{session, prompt})
	if session != "" && session == f.failOn {
		return reviewAnswer{}, errors.New("session expired")
	}
	reply := f.replies[0]
	f.replies = f.replies[1:]
	id := session
	if id == "" {
		id = "sess-" + string(rune('a'+len(f.calls)-1))
	}
	return reviewAnswer{session: id, text: reply}, nil
}

type reviewFixture struct {
	git      *fakeGit
	def, sec *fakeReviewer
	deps     reviewDeps
	tickets  []string
}

func newReviewFixture(t *testing.T) *reviewFixture {
	fx := &reviewFixture{
		git: &fakeGit{head: "c1", tree: "t1", base: "b0", files: []string{"cmd/x.go"}, messages: "feat: x\n"},
		def: &fakeReviewer{},
		sec: &fakeReviewer{},
	}
	fx.deps = reviewDeps{
		root: t.TempDir(),
		git:  fx.git.run,
		showTicket: func(_ context.Context, id string) (string, error) {
			fx.tickets = append(fx.tickets, id)
			return id + ": the ticket", nil
		},
		standards: func(context.Context, string) (string, error) { return "STANDARDS TEXT", nil },
		reviewer: func(model string) (reviewer, error) {
			if model == "fake-security" {
				return fx.sec, nil
			}
			return fx.def, nil
		},
		recordDir: t.TempDir(),
		now:       func() time.Time { return time.Unix(0, 0) },
		logf:      func(string, ...any) {},
	}
	return fx
}

func TestReviewResumesTheSameSessionAndClearsFixedFindings(t *testing.T) {
	fx := newReviewFixture(t)
	p := testPolicy(t)
	fx.def.replies = []string{
		`{"findings": [{"id": "F1", "severity": "P1", "rule": "tenant-isolation", "file": "cmd/x.go:3", "summary": "leaks team ids"}]}`,
		`{"findings": [{"id": "F1", "severity": "P1", "rule": "tenant-isolation", "file": "cmd/x.go:3", "summary": "resolved now", "status": "fixed"}]}`,
	}
	in := reviewInputs{branch: "bw" + "-42-thing"}
	rec, file, err := runReview(context.Background(), fx.deps, p, in)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Result != resultBlock || rec.BlockingOpen != 1 || rec.Reviewer.Session != "sess-a" {
		t.Fatalf("first pass: %+v", rec)
	}
	if rec.Objective.Source != "ticket" || rec.Objective.Ref != "BW"+"-42" {
		t.Errorf("objective %+v, want ticket BW"+"-42", rec.Objective)
	}
	first := fx.def.calls[0].prompt
	for _, want := range []string{"STANDARDS TEXT", "BW" + "-42: the ticket", "diff of everything at c1", "tenant-isolation", "| P1 |", "never block", "billing, blocks at P2"} {
		if !strings.Contains(strings.ToLower(first), strings.ToLower(want)) {
			t.Errorf("first prompt lacks %q", want)
		}
	}

	fx.git.head, fx.git.tree = "c2", "t2"
	in.brief = "Lands and scans now run on our own hardware."
	rec, _, err = runReview(context.Background(), fx.deps, p, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.def.calls) != 2 || fx.def.calls[1].session != "sess-a" {
		t.Fatalf("second pass did not resume sess-a: %+v", fx.def.calls)
	}
	second := fx.def.calls[1].prompt
	if !strings.Contains(second, `"id": "F1"`) || !strings.Contains(second, "diff of everything at c2") || !strings.Contains(second, in.brief) {
		t.Errorf("resume prompt lacks the open finding or the new diff:\n%s", second)
	}
	if rec.Result != resultPass || rec.Findings[0].Status != findingFixed || rec.Findings[0].Summary != "leaks team ids" || len(rec.Passes) != 2 || !rec.Passes[1].Resumed {
		t.Errorf("second pass: %+v", rec)
	}
	saved, err := loadReviewRecord(file)
	if err != nil || saved == nil || saved.Result != resultPass || saved.Tree != "t2" {
		t.Errorf("saved record %+v, %v", saved, err)
	}
}

func TestReviewUnchangedTreeOnlyReappliesSkips(t *testing.T) {
	fx := newReviewFixture(t)
	p := testPolicy(t)
	fx.def.replies = []string{`{"findings": [{"id": "F1", "severity": "P2", "file": "cmd/x.go:1", "summary": "slow"}, {"id": "F2", "severity": "P1", "file": "cmd/x.go:2", "summary": "wrong"}]}`}
	in := reviewInputs{branch: "feature/a"}
	if _, _, err := runReview(context.Background(), fx.deps, p, in); err != nil {
		t.Fatal(err)
	}
	in.skip = "F1=measured, not hot; F2=disagree"
	rec, _, err := runReview(context.Background(), fx.deps, p, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.def.calls) != 1 {
		t.Errorf("an unchanged tree called the reviewer again")
	}
	if rec.Findings[0].Status != findingSkipped || rec.Findings[1].Status != findingOpen || rec.Result != resultBlock {
		t.Errorf("record %+v", rec)
	}
}

func TestReviewRunsTheSecurityReviewerOnStrictPaths(t *testing.T) {
	fx := newReviewFixture(t)
	p := testPolicy(t)
	fx.git.files = []string{"cmd/x.go", "internal/store/billing_policy.go"}
	fx.def.replies = []string{`{"findings": []}`}
	fx.sec.replies = []string{`{"findings": [{"id": "S1", "severity": "P2", "file": "internal/store/billing_policy.go:7", "summary": "rounding"}]}`}
	rec, _, err := runReview(context.Background(), fx.deps, p, reviewInputs{branch: "feature/b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.sec.calls) != 1 || !strings.Contains(fx.sec.calls[0].prompt, "diff of internal/store/billing_policy.go") {
		t.Fatalf("security reviewer calls %+v", fx.sec.calls)
	}
	if strings.Contains(fx.sec.calls[0].prompt, "cmd/x.go") {
		t.Errorf("security reviewer saw a non-strict file")
	}
	if rec.Result != resultBlock || len(rec.Reviewers) != 2 || rec.Reviewer.Profile != "default" {
		t.Errorf("record %+v", rec)
	}
	if rec.session("security").Session == "" {
		t.Errorf("security session not recorded: %+v", rec.Reviewers)
	}
}

func TestReviewRoutesDocsOnlyDiffsToTheDocsModel(t *testing.T) {
	fx := newReviewFixture(t)
	p := testPolicy(t)
	p.DocsOnly = docsOnlyRoute{Paths: []string{"docs/**", "**/*.md"}, Reviewer: "docs"}
	p.Reviewers["docs"] = reviewerProfile{Model: "fake-docs"}
	var models []string
	reviewerFor := fx.deps.reviewer
	fx.deps.reviewer = func(model string) (reviewer, error) {
		models = append(models, model)
		return reviewerFor(model)
	}
	fx.git.files = []string{"docs/a.md", "README.md"}
	fx.def.replies = []string{`{"findings": []}`, `{"findings": []}`}
	in := reviewInputs{branch: "feature/docs"}
	rec, _, err := runReview(context.Background(), fx.deps, p, in)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Reviewer.Profile != defaultReviewer || rec.Reviewer.Model != "fake-docs" {
		t.Errorf("docs-only record reviewer %+v, want default on fake-docs", rec.Reviewer)
	}

	fx.git.head, fx.git.tree = "c2", "t2"
	fx.git.files = []string{"docs/a.md", "cmd/x.go"}
	rec, _, err = runReview(context.Background(), fx.deps, p, in)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Reviewer.Model != "fake-default" || fx.def.calls[1].session != "" {
		t.Errorf("mixed diff reviewer %+v, call %+v; want a fresh fake-default session", rec.Reviewer, fx.def.calls[1])
	}
	if strings.Join(models, ",") != "fake-docs,fake-default" {
		t.Errorf("models %v", models)
	}
}

func TestReviewFallsBackToANewSessionWhenResumeFails(t *testing.T) {
	fx := newReviewFixture(t)
	p := testPolicy(t)
	fx.def.replies = []string{
		`{"findings": [{"id": "F1", "severity": "P1", "file": "cmd/x.go:1", "summary": "bad"}]}`,
		`{"findings": [{"id": "F1", "severity": "P1", "file": "cmd/x.go:1", "summary": "bad", "status": "fixed"}]}`,
	}
	fx.def.failOn = "sess-a"
	in := reviewInputs{branch: "feature/c"}
	if _, _, err := runReview(context.Background(), fx.deps, p, in); err != nil {
		t.Fatal(err)
	}
	fx.git.tree = "t2"
	rec, _, err := runReview(context.Background(), fx.deps, p, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.def.calls) != 3 || fx.def.calls[2].session != "" || !strings.Contains(fx.def.calls[2].prompt, "Earlier findings") {
		t.Fatalf("calls %+v", fx.def.calls)
	}
	if rec.Result != resultPass || !strings.Contains(strings.Join(rec.Notes, "\n"), "resume of default session sess-a failed") {
		t.Errorf("record %+v", rec)
	}
}

func TestObjectiveResolutionOrder(t *testing.T) {
	p := testPolicy(t)
	for _, c := range []struct {
		name, branch, messages, brief string
		ticketErr                     bool
		want                          string
	}{
		{"brief wins", "bw" + "-9-x", "feat: x (BW" + "-9)", "the brief", false, "brief"},
		{"branch ticket", "bw" + "-9-x", "feat: x", "", false, "ticket"},
		{"trailer ticket", "x", "feat: x\n\nTickets: BW" + "-9\n", "", false, "ticket"},
		{"subject ticket", "x", "feat: x (BW" + "-9)\n", "", false, "ticket"},
		{"ticket unreadable", "bw" + "-9-x", "feat: x", "", true, "commit-message"},
		{"commit message", "x", "feat: x", "", false, "commit-message"},
		{"brief only", "x", "", "the brief", false, "brief"},
		{"none", "x", "", "", false, "none"},
	} {
		fx := newReviewFixture(t)
		fx.git.messages = c.messages
		if c.ticketErr {
			fx.deps.showTicket = func(context.Context, string) (string, error) { return "", errors.New("offline") }
		}
		got, _ := resolveObjective(context.Background(), fx.deps, p, reviewInputs{branch: c.branch, brief: c.brief}, "b0")
		if got.Source != c.want {
			t.Errorf("%s: source %s, want %s", c.name, got.Source, c.want)
		}
	}
}

func TestCodexEvents(t *testing.T) {
	out := []byte(`{"type":"thread.started","thread_id":"019a-uuid"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"looking"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","text":"git diff"}}
{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"{\"findings\": []}"}}
{"type":"turn.completed"}
`)
	thread, last := codexEvents(out)
	if thread != "019a-uuid" || last != `{"findings": []}` {
		t.Errorf("thread %q, last message %q", thread, last)
	}
}

func TestReviewClarifiedObjectiveReassessesUnchangedTree(t *testing.T) {
	fx := newReviewFixture(t)
	p := testPolicy(t)
	fx.def.replies = []string{
		`{"findings":[{"id":"F1","severity":"P1","rule":"objective-not-met","summary":"umbrella incomplete"}]}`,
		`{"findings":[{"id":"F1","severity":"P1","status":"fixed","summary":"scope clarified"}]}`,
	}
	in := reviewInputs{branch: "bw" + "-42-umbrella"}
	first, _, err := runReview(context.Background(), fx.deps, p, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Result != resultBlock || first.Objective.Source != "ticket" {
		t.Fatalf("first review: %+v", first)
	}
	in.brief = "Fix shell escaping only; the umbrella's other work lands separately."
	rec, _, err := runReview(context.Background(), fx.deps, p, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.def.calls) != 2 || rec.Result != resultPass || rec.Objective.Source != "brief" || rec.Tree != first.Tree {
		t.Fatalf("clarification did not replace block: calls=%d record=%+v", len(fx.def.calls), rec)
	}
	if !strings.Contains(fx.def.calls[1].prompt, in.brief) || !strings.Contains(fx.def.calls[1].prompt, "This objective replaces earlier objectives") {
		t.Fatal("reviewer did not receive superseding objective")
	}
	in.brief = "Fix and document shell escaping only."
	fx.def.replies = append(fx.def.replies, `{"findings":[]}`)
	if _, _, err := runReview(context.Background(), fx.deps, p, in); err != nil {
		t.Fatal(err)
	}
	if len(fx.def.calls) != 3 || !strings.Contains(fx.def.calls[2].prompt, in.brief) {
		t.Fatal("changed brief did not rerun review")
	}
	if _, _, err := runReview(context.Background(), fx.deps, p, in); err != nil {
		t.Fatal(err)
	}
	if len(fx.def.calls) != 3 {
		t.Fatal("unchanged brief did not reuse saved verdict")
	}
}
