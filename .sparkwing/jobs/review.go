package jobs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// ReviewArgs are the author's inputs to a review run.
type ReviewArgs struct {
	Brief string `flag:"brief" desc:"The change's objective; when set it wins over the ticket and the commit messages."`
	Skip  string `flag:"skip" desc:"Findings to skip with a reason, as 'F3=reason; F4=reason'. Only P2 and P3 findings off strict paths may be skipped."`
}

// Review runs the AI code review that .review.yaml describes against the
// branch's diff from origin/main, and fails when a blocking finding is open.
type Review struct{ sparkwing.Base }

func (Review) ShortHelp() string {
	return "AI review of the branch against origin/main, under the rules in .review.yaml"
}

func (Review) Help() string {
	return "Reads .review.yaml, resolves the change's objective (--brief when given, then the bw-<n> or gh-<n> ticket from the branch name, " +
		"a Tickets trailer or a (BW-<n>) or (GH-<n>) commit subject, then the commit messages), and asks the default reviewer, plus the security reviewer when the diff " +
		"touches a strict path, for JSON findings. The record lands in <git common dir>/sparkwing-review/<branch>.json " +
		"and is the job's output. A re-run on the same branch resumes each reviewer's session so it re-checks its open " +
		"findings; an unchanged tree reuses the record without a model call. The run fails when a P0 or P1, or a P2 on a " +
		"strict path, is open."
}

func (Review) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Review the current branch", Command: "sparkwing run review"},
		{Comment: "Skip a P2 finding with a reason", Command: "sparkwing run review -- --skip 'F2=covered by the store migration test'"},
	}
}

func (p *Review) Plan(_ context.Context, plan *sparkwing.Plan, in ReviewArgs, run sparkwing.RunContext) error {
	branch := ""
	if run.Git != nil {
		branch = run.Git.Branch
	}
	reviewJobNode(plan, in, branch)
	return nil
}

// safety: land declares its review through this function too, so the review it
// requires before pushing main is the job `sparkwing run review` runs.
func reviewJobNode(plan *sparkwing.Plan, in ReviewArgs, branch string) *sparkwing.JobNode {
	return sparkwing.Job(plan, "review", &reviewJob{args: in, branch: branch}).
		Inline().
		Timeout(45 * time.Minute).
		NoProgressTimeout(30 * time.Minute).
		Cores(lightJobCores)
}

func init() {
	sparkwing.Register[ReviewArgs]("Review", func() sparkwing.Pipeline[ReviewArgs] { return &Review{} })
}

type reviewJob struct {
	sparkwing.Base
	sparkwing.Produces[reviewRecord]
	args   ReviewArgs
	branch string
}

func (j *reviewJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "review", j.run), nil
}

func (j *reviewJob) run(ctx context.Context) (reviewRecord, error) {
	root := sourceTreeRoot()
	if root == "" {
		return reviewRecord{}, errors.New("review: cannot find the repository root")
	}
	data, err := os.ReadFile(filepath.Join(root, ".review.yaml"))
	if err != nil {
		return reviewRecord{}, fmt.Errorf("review: read .review.yaml: %w", err)
	}
	policy, err := parseReviewPolicy(data)
	if err != nil {
		return reviewRecord{}, err
	}
	git := func(ctx context.Context, args ...string) (string, error) {
		out, err := sparkwing.Exec(ctx, "git", args...).Dir(root).String()
		return strings.TrimSpace(out), err
	}
	common, err := git(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return reviewRecord{}, fmt.Errorf("review: locate the git common dir: %w", err)
	}
	branch := j.branch
	if branch == "" {
		if b, err := git(ctx, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
			branch = b
		}
	}
	deps := reviewDeps{
		root:       root,
		git:        git,
		showTicket: showTicket,
		standards:  func(ctx context.Context, diff string) (string, error) { return overwingStandards(ctx, root, diff) },
		reviewer:   newReviewer,
		recordDir:  filepath.Join(common, "sparkwing-review"),
		now:        time.Now,
		logf:       func(format string, args ...any) { sparkwing.Info(ctx, format, args...) },
	}
	rec, file, err := runReview(ctx, deps, policy, reviewInputs{branch: branch, brief: j.args.Brief, skip: j.args.Skip})
	if err != nil {
		return reviewRecord{}, err
	}
	summary := reviewSummary(rec, file)
	sparkwing.Summary(ctx, summary)
	if rec.Result == resultBlock {
		return rec, fmt.Errorf("review blocked: %d blocking finding(s) open\n\n%s", rec.BlockingOpen, summary)
	}
	return rec, nil
}

type reviewDeps struct {
	root       string
	git        func(ctx context.Context, args ...string) (string, error)
	showTicket func(ctx context.Context, id string) (string, error)
	standards  func(ctx context.Context, diff string) (string, error)
	reviewer   func(model string) (reviewer, error)
	recordDir  string
	now        func() time.Time
	logf       func(format string, args ...any)
}

type reviewInputs struct {
	branch string
	brief  string
	skip   string
}

const (
	reviewBaseRef = "origin/main"
	maxPromptDiff = 300 << 10
)

var findingPrefix = map[string]string{defaultReviewer: "F", "security": "S"}

func runReview(ctx context.Context, d reviewDeps, p reviewPolicy, in reviewInputs) (reviewRecord, string, error) {
	started := d.now()
	skips, err := parseSkips(in.skip)
	if err != nil {
		return reviewRecord{}, "", err
	}
	head, err := d.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return reviewRecord{}, "", fmt.Errorf("review: resolve HEAD: %w", err)
	}
	tree, err := d.git(ctx, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return reviewRecord{}, "", fmt.Errorf("review: resolve the tree: %w", err)
	}
	base, err := d.git(ctx, "merge-base", reviewBaseRef, "HEAD")
	if err != nil {
		return reviewRecord{}, "", fmt.Errorf("review: no merge base with %s; fetch it first: %w", reviewBaseRef, err)
	}
	branch := in.branch
	if branch == "" {
		branch = "detached-" + short(head)
	}
	file := reviewRecordPath(d.recordDir, branch)
	prior, err := loadReviewRecord(file)
	if err != nil {
		return reviewRecord{}, "", err
	}
	rec := reviewRecord{Version: 1, Branch: branch}
	if prior != nil {
		rec = *prior
	}
	rec.Notes = nil
	sameTree := prior != nil && prior.Tree == tree
	rec.Tree, rec.Commit, rec.Base = tree, head, base
	d.logf("review timing: phase=resolve elapsed=%s reused=%t", d.now().Sub(started), sameTree)

	if sameTree {
		rec.Notes = append(rec.Notes, "tree unchanged since the last review, so no reviewer ran; skips were reapplied")
	} else if err := reviewPassForTree(ctx, d, p, &rec, in, base, head, tree); err != nil {
		return reviewRecord{}, "", err
	}
	started = d.now()
	rec.Notes = append(rec.Notes, applySkips(p, rec.Findings, skips)...)
	rec.settle(p)
	if err := saveReviewRecord(file, rec); err != nil {
		return reviewRecord{}, "", fmt.Errorf("review: write the record: %w", err)
	}
	d.logf("review timing: phase=record elapsed=%s", d.now().Sub(started))
	return rec, file, nil
}

func reviewPassForTree(ctx context.Context, d reviewDeps, p reviewPolicy, rec *reviewRecord, in reviewInputs, base, head, tree string) error {
	started := d.now()
	diff, err := d.git(ctx, "diff", "--no-color", "--no-ext-diff", base, head)
	if err != nil {
		return fmt.Errorf("review: diff %s..%s: %w", short(base), short(head), err)
	}
	names, err := d.git(ctx, "diff", "--name-only", "--no-ext-diff", base, head)
	if err != nil {
		return fmt.Errorf("review: list changed files: %w", err)
	}
	files := strings.Fields(names)
	d.logf("review timing: phase=diff elapsed=%s bytes=%d files=%d", d.now().Sub(started), len(diff), len(files))
	started = d.now()
	objective, notes := resolveObjective(ctx, d, p, in, base)
	rec.Objective = objective
	rec.Notes = append(rec.Notes, notes...)
	pass := reviewPass{Tree: tree, Commit: head, At: d.now().UTC()}

	profiles := map[string][]string{defaultReviewer: files}
	for profile, strictFiles := range p.strictReviewers(files) {
		profiles[profile] = strictFiles
	}
	for _, ref := range rec.Reviewers {
		if _, ok := profiles[ref.Profile]; !ok && rec.openBy(ref.Profile) {
			profiles[ref.Profile] = nil
		}
	}
	if len(files) == 0 {
		for profile := range profiles {
			if !rec.openBy(profile) {
				delete(profiles, profile)
			}
		}
	}
	if len(profiles) == 0 {
		rec.Notes = append(rec.Notes, "no changes against "+reviewBaseRef+", so no reviewer ran")
		rec.Passes = append(rec.Passes, pass)
		return nil
	}
	standards := ""
	if p.usesStandards() {
		if standards, err = d.standards(ctx, diff); err != nil {
			rec.Notes = append(rec.Notes, "house standards unavailable, so the standards rule was judged without them: "+err.Error())
			standards = ""
		}
	}
	d.logf("review timing: phase=context elapsed=%s standards_bytes=%d", d.now().Sub(started), len(standards))
	started = d.now()
	order := make([]string, 0, len(profiles))
	for profile := range profiles {
		order = append(order, profile)
	}
	sort.Strings(order)
	docsModel := p.defaultModel(files)
	if docsModel != p.Reviewers[defaultReviewer].Model {
		rec.Notes = append(rec.Notes, "every changed file is documentation, so the default reviewer ran on "+docsModel)
	}
	type profileResult struct {
		record  reviewRecord
		resumed bool
		err     error
	}
	results := make([]profileResult, len(order))
	prompts := make([]reviewPrompt, len(order))
	for i, profile := range order {
		scoped := diff
		if profile != defaultReviewer {
			if scoped, err = scopedDiff(ctx, d, base, head, profiles[profile]); err != nil {
				return err
			}
		}
		prompts[i] = reviewPrompt{
			policy: p, profile: profile, objective: objective, standards: standards,
			base: base, head: head, diff: scoped, files: profiles[profile],
		}
		results[i].record = reviewRecord{
			Findings:  slices.Clone(rec.Findings),
			Reviewers: slices.Clone(rec.Reviewers),
		}
	}
	d.logf("review timing: phase=prompts elapsed=%s profiles=%d", d.now().Sub(started), len(order))
	var wg sync.WaitGroup
	for i, profile := range order {
		wg.Add(1)
		go func() {
			defer wg.Done()
			model := p.Reviewers[profile].Model
			if profile == defaultReviewer {
				model = docsModel
			}
			result := &results[i]
			result.resumed, result.err = runProfile(ctx, d, p, &result.record, profile, model, prompts[i])
		}()
	}
	wg.Wait()
	started = d.now()
	var errs []error
	for _, result := range results {
		errs = append(errs, result.err)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	for i, profile := range order {
		result := &results[i]
		rec.Findings = mergeFindings(rec.Findings, findingsBy(result.record.Findings, profile), profile, prefixFor(profile))
		rec.setSession(result.record.session(profile))
		rec.Notes = append(rec.Notes, result.record.Notes...)
		pass.Profiles = append(pass.Profiles, profile)
		pass.Resumed = pass.Resumed || result.resumed
	}

	rec.Passes = append(rec.Passes, pass)
	d.logf("review timing: phase=settle elapsed=%s", d.now().Sub(started))
	return nil
}

func scopedDiff(ctx context.Context, d reviewDeps, base, head string, files []string) (string, error) {
	if len(files) == 0 {
		return "", nil
	}
	args := append([]string{"diff", "--no-color", "--no-ext-diff", base, head, "--"}, files...)
	out, err := d.git(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("review: diff the strict paths: %w", err)
	}
	return out, nil
}

func runProfile(ctx context.Context, d reviewDeps, p reviewPolicy, rec *reviewRecord, profile, model string, prompt reviewPrompt) (bool, error) {
	rv, err := d.reviewer(model)
	if err != nil {
		return false, err
	}
	prev := rec.session(profile)
	prompt.prior = findingsBy(rec.Findings, profile)
	resume := p.Rereview.SameReviewer && prev.Session != "" && prev.Model == model
	call := func(session, text, mode string) (reviewAnswer, error) {
		started := d.now()
		answer, err := rv.review(ctx, d.root, session, text)
		d.logf("review timing: phase=model profile=%s model=%s mode=%s elapsed=%s prompt_bytes=%d failed=%t", profile, model, mode, d.now().Sub(started), len(text), err != nil)
		return answer, err
	}
	var ans reviewAnswer
	if resume {
		d.logf("review: resuming the %s reviewer (%s) session %s", profile, model, prev.Session)
		ans, err = call(prev.Session, prompt.rereview(), "resume")
		if err != nil {
			rec.Notes = append(rec.Notes, fmt.Sprintf("resume of %s session %s failed, so a new session re-checked its findings: %v", profile, prev.Session, err))
			resume = false
		}
	}
	if !resume {
		d.logf("review: starting the %s reviewer (%s)", profile, model)
		if ans, err = call("", prompt.fresh(), "fresh"); err != nil {
			return false, fmt.Errorf("review: %s reviewer (%s): %w", profile, model, err)
		}
	}
	reply, err := parseReviewReply(ans.text)
	if err != nil {
		return resume, fmt.Errorf("review: %s reviewer (%s): %w", profile, model, err)
	}
	rec.Findings = mergeFindings(rec.Findings, reply, profile, prefixFor(profile))
	rec.setSession(reviewerRef{Profile: profile, Model: model, Session: ans.session})
	return resume, nil
}

func prefixFor(profile string) string {
	if p, ok := findingPrefix[profile]; ok {
		return p
	}
	return strings.ToUpper(profile[:1])
}

func findingsBy(all []reviewFinding, profile string) []reviewFinding {
	var out []reviewFinding
	for _, f := range all {
		if f.Reviewer == profile {
			out = append(out, f)
		}
	}
	return out
}

func (p reviewPolicy) usesStandards() bool {
	for _, r := range p.Rules {
		if r.From == standardsRuleFrom {
			return true
		}
	}
	return false
}

var (
	ticketBranchRE  = regexp.MustCompile(`(?i)^(bw|gh)-(\d+)(?:-|$)`)
	ticketTrailerRE = regexp.MustCompile(`(?mi)^Tickets?:[ \t]*((?:BW|GH)-\d+)`)
	ticketSubjectRE = regexp.MustCompile(`(?i)\(((?:BW|GH)-\d+)\)`)
)

func ticketFrom(branch, messages, subjects string) string {
	if m := ticketBranchRE.FindStringSubmatch(branch); m != nil {
		return strings.ToUpper(m[1]) + "-" + m[2]
	}
	if m := ticketTrailerRE.FindStringSubmatch(messages); m != nil {
		return strings.ToUpper(m[1])
	}
	if m := ticketSubjectRE.FindStringSubmatch(subjects); m != nil {
		return strings.ToUpper(m[1])
	}
	return ""
}

func resolveObjective(ctx context.Context, d reviewDeps, p reviewPolicy, in reviewInputs, base string) (reviewObjective, []string) {
	var notes []string
	messages, err := d.git(ctx, "log", "--format=%B%n", base+"..HEAD")
	if err != nil {
		notes = append(notes, "commit messages unreadable: "+err.Error())
		messages = ""
	}
	subjects, err := d.git(ctx, "log", "--format=%s", base+"..HEAD")
	if err != nil {
		subjects = ""
	}
	if brief := strings.TrimSpace(in.brief); brief != "" && slices.Contains(p.Objective.Sources, "brief") {
		return reviewObjective{Source: "brief", text: brief}, notes
	}
	for _, source := range p.Objective.Sources {
		switch source {
		case "ticket":
			id := ticketFrom(in.branch, messages, subjects)
			if id == "" {
				continue
			}
			text, err := d.showTicket(ctx, id)
			if err != nil {
				notes = append(notes, fmt.Sprintf("ticket %s unreadable, so the next objective source was used: %v", id, err))
				continue
			}
			return reviewObjective{Source: "ticket", Ref: id, text: text}, notes
		case "commit-message":
			if strings.TrimSpace(messages) != "" {
				return reviewObjective{Source: "commit-message", Ref: "HEAD", text: strings.TrimSpace(messages)}, notes
			}
		}
	}
	return reviewObjective{Source: "none"}, append(notes, "no objective resolved, so the objective-not-met rule could not fire")
}

const maxTicketBody = 12 << 10

func showTicket(ctx context.Context, id string) (string, error) {
	var out string
	var err error
	if num, ok := strings.CutPrefix(id, "GH-"); ok {
		out, err = sparkwing.Exec(ctx, "gh", "issue", "view", num, "--json", "title,body").String()
	} else {
		out, err = sparkwing.Exec(ctx, "bitwing", "ticket", "show", "-i", id, "-o", "json").String()
	}
	if err != nil {
		return "", err
	}
	var t struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &t); err != nil {
		return "", fmt.Errorf("%s: no ticket JSON: %w", id, err)
	}
	return fmt.Sprintf("%s: %s\n\n%s", id, t.Title, clip(t.Body, maxTicketBody)), nil
}

func overwingStandards(ctx context.Context, root, diff string) (string, error) {
	match, err := runWithStdin(ctx, root, diff, nil, "overwing", "standards", "match", "--repo", root, "--role", "reviewer", "--diff-file", "-", "-o", "json")
	if err != nil {
		return "", err
	}
	names := standardCardNames(string(match))
	if len(names) == 0 {
		return "", nil
	}
	cards, err := sparkwing.Exec(ctx, "overwing", append([]string{"standards", "show"}, append(names, "-o", "json")...)...).String()
	if err != nil {
		return "", err
	}
	return reviewStandardBodies(cards)
}

var standardBacklink = regexp.MustCompile(`^- \[\[[^\]]+\]\] \x{2014} (ai|semantic)$`)

// perf: routing metadata and vault backlinks do not instruct the reviewer.
func reviewStandardBodies(cards string) (string, error) {
	dec := json.NewDecoder(strings.NewReader(cards))
	var bodies []string
	for {
		var card struct {
			Contents string `json:"contents"`
		}
		if err := dec.Decode(&card); err != nil {
			if errors.Is(err, io.EOF) {
				return strings.Join(bodies, "\n\n"), nil
			}
			return "", fmt.Errorf("review: decode standards card: %w", err)
		}
		body := card.Contents
		if strings.HasPrefix(body, "---\n") {
			if metadata, rest, ok := strings.Cut(body[4:], "\n---\n"); ok && !strings.Contains(metadata, "lint_rules:") {
				body = rest
			}
		}
		if rules, related, ok := strings.Cut(body, "\n## Related\n"); ok {
			// safety: remove only a terminal section of vault backlinks.
			linksOnly := true
			for _, line := range strings.Split(related, "\n") {
				line = strings.TrimSpace(line)
				linksOnly = linksOnly && (line == "" || standardBacklink.MatchString(line))
			}
			if linksOnly {
				body = rules
			}
		}
		bodies = append(bodies, strings.TrimSpace(body))
	}
}

func standardCardNames(ndjson string) []string {
	var names []string
	sc := bufio.NewScanner(strings.NewReader(ndjson))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var rec struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.Kind == "card" && rec.Name != "" {
			names = append(names, rec.Name)
		}
	}
	return names
}

type reviewPrompt struct {
	policy    reviewPolicy
	profile   string
	objective reviewObjective
	standards string
	base      string
	head      string
	diff      string
	files     []string
	prior     []reviewFinding
}

const severityTable = `| Severity | Meaning | Blocks |
| --- | --- | --- |
| P0 | Objective not met, or a security or data-loss risk that is likely or severe | yes |
| P1 | Significant security, correctness or usability risk; or a design-rule violation | yes |
| P2 | Real but bounded issue; fixing it is worthwhile | no, except on a strict path at or below its block_at |
| P3 | Suggestion, clarity, minor cleanup | no |`

func (r reviewPrompt) fresh() string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are the %q code reviewer for this repository. Review the change from %s to %s under the policy below. ", r.profile, short(r.base), short(r.head))
	b.WriteString("Do not edit files. You may read the repository and run read-only git commands.\n\n")
	b.WriteString("## Severities\n\n" + severityTable + "\n\n")
	b.WriteString("Block only when the change fails its objective, carries a significant security or usability risk, or breaks a design rule below. Nits, style and preference never block. Rare, non-catastrophic, recoverable edge cases never block; report them as P2 or P3 at most.\n\n")
	if len(r.policy.NeverBlock) > 0 {
		b.WriteString("## Never block\n\n")
		for _, n := range r.policy.NeverBlock {
			b.WriteString("- " + n + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## Design rules (a violation is P1; cite the rule id)\n\n")
	for _, rule := range r.policy.Rules {
		if rule.From == standardsRuleFrom {
			fmt.Fprintf(&b, "- %s: the house standards below.\n", rule.ID)
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", rule.ID, rule.Text)
	}
	b.WriteString("\n")
	if len(r.policy.Strict) > 0 {
		b.WriteString("## Strict paths (findings here block at the listed severity)\n\n")
		for _, s := range r.policy.Strict {
			fmt.Fprintf(&b, "- %s, blocks at %s: %s\n", s.Name, s.BlockAt, strings.Join(s.Paths, ", "))
		}
		b.WriteString("\n")
	}
	if r.profile != defaultReviewer {
		fmt.Fprintf(&b, "You review only these strict-path files; another reviewer covers the rest: %s\n\n", strings.Join(r.files, ", "))
	}
	if strings.TrimSpace(r.standards) != "" {
		b.WriteString("## House standards\n\n" + r.standards + "\n\n")
	}
	b.WriteString("## Objective\n\n")
	if r.objective.text == "" {
		b.WriteString("No objective was supplied, so the objective-not-met rule cannot fire.\n\n")
	} else {
		fmt.Fprintf(&b, "Source: %s %s\n\n%s\n\n", r.objective.Source, r.objective.Ref, r.objective.text)
	}
	if len(r.prior) > 0 {
		b.WriteString("## Earlier findings\n\nAn earlier review session raised these. Re-check each against the diff and keep its id.\n\n")
		b.WriteString(findingsJSON(r.prior) + "\n\n")
	}
	b.WriteString(r.diffSection())
	b.WriteString(r.answerFormat())
	return b.String()
}

func (r reviewPrompt) rereview() string {
	var b strings.Builder
	fmt.Fprintf(&b, "The author changed the branch; it now runs from %s to %s. Re-check your findings below against the new diff under the same policy. ", short(r.base), short(r.head))
	b.WriteString("Mark a finding fixed only when the diff resolves it; keep it open otherwise, and keep every id. Raise new findings for new problems. Do not edit files.\n\n")
	b.WriteString("## Current objective\n\nThis objective replaces earlier objectives. Reassess objective findings against it; the review policy still applies.\n\n")
	if r.objective.text == "" {
		b.WriteString("No objective was supplied, so the objective-not-met rule cannot fire.\n\n")
	} else {
		fmt.Fprintf(&b, "Source: %s %s\n\n%s\n\n", r.objective.Source, r.objective.Ref, r.objective.text)
	}
	b.WriteString("## Your findings\n\n" + findingsJSON(r.prior) + "\n\n")
	b.WriteString(r.diffSection())
	b.WriteString(r.answerFormat())
	return b.String()
}

func (r reviewPrompt) diffSection() string {
	diff := r.diff
	note := ""
	if len(diff) > maxPromptDiff {
		diff = diff[:maxPromptDiff]
		note = fmt.Sprintf("The diff was cut at %d bytes; read the rest with `git diff %s %s`.\n\n", maxPromptDiff, r.base, r.head)
	}
	return "## Diff\n\n" + note + "```diff\n" + diff + "\n```\n\n"
}

func (r reviewPrompt) answerFormat() string {
	prefix := prefixFor(r.profile)
	return "## Answer\n\nReply with one JSON object and nothing else:\n\n" +
		"{\"findings\": [{\"id\": \"" + prefix + "1\", \"severity\": \"P0|P1|P2|P3\", \"rule\": \"<rule id, or empty>\", " +
		"\"file\": \"path:line\", \"summary\": \"<what is wrong and why it matters>\", \"status\": \"open|fixed\"}]}\n\n" +
		"Give new findings ids starting with " + prefix + ". Return every finding you still hold, fixed ones included. An empty findings array means the change passes.\n"
}

func findingsJSON(fs []reviewFinding) string {
	type view struct {
		ID       string `json:"id"`
		Severity string `json:"severity"`
		Rule     string `json:"rule,omitempty"`
		File     string `json:"file,omitempty"`
		Summary  string `json:"summary"`
		Status   string `json:"status"`
	}
	out := make([]view, 0, len(fs))
	for _, f := range fs {
		out = append(out, view{f.ID, f.Severity, f.Rule, f.File, f.Summary, f.Status})
	}
	data, err := json.MarshalIndent(map[string]any{"findings": out}, "", "  ")
	if err != nil {
		return err.Error()
	}
	return string(data)
}
