package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type reviewFinding struct {
	ID         string `json:"id"`
	Severity   string `json:"severity"`
	Rule       string `json:"rule,omitempty"`
	File       string `json:"file,omitempty"`
	Summary    string `json:"summary"`
	Status     string `json:"status"`
	SkipReason string `json:"skip_reason,omitempty"`
	Reviewer   string `json:"reviewer,omitempty"`
}

type reviewObjective struct {
	Source string `json:"source"`
	Ref    string `json:"ref,omitempty"`
	text   string
}

type reviewerRef struct {
	Profile string `json:"profile"`
	Model   string `json:"model"`
	Session string `json:"session,omitempty"`
}

type reviewPass struct {
	Tree     string    `json:"tree"`
	Commit   string    `json:"commit"`
	At       time.Time `json:"at"`
	Profiles []string  `json:"profiles,omitempty"`
	Resumed  bool      `json:"resumed,omitempty"`
}

// safety: reviewer and findings[].reviewer extend the spec's single-reviewer
// record, so the security profile's session and findings stay distinct.
type reviewRecord struct {
	Version      int             `json:"version"`
	Branch       string          `json:"branch"`
	Tree         string          `json:"tree"`
	Commit       string          `json:"commit"`
	Base         string          `json:"base"`
	Objective    reviewObjective `json:"objective"`
	Reviewer     reviewerRef     `json:"reviewer"`
	Reviewers    []reviewerRef   `json:"reviewers,omitempty"`
	Findings     []reviewFinding `json:"findings"`
	BlockingOpen int             `json:"blocking_open"`
	Result       string          `json:"result"`
	Notes        []string        `json:"notes,omitempty"`
	Passes       []reviewPass    `json:"passes,omitempty"`
}

func (r *reviewRecord) session(profile string) reviewerRef {
	for _, ref := range r.Reviewers {
		if ref.Profile == profile {
			return ref
		}
	}
	return reviewerRef{}
}

func (r *reviewRecord) setSession(ref reviewerRef) {
	for i := range r.Reviewers {
		if r.Reviewers[i].Profile == ref.Profile {
			r.Reviewers[i] = ref
			r.syncDefault()
			return
		}
	}
	r.Reviewers = append(r.Reviewers, ref)
	sort.Slice(r.Reviewers, func(i, j int) bool {
		return (r.Reviewers[i].Profile == defaultReviewer) || (r.Reviewers[j].Profile != defaultReviewer && r.Reviewers[i].Profile < r.Reviewers[j].Profile)
	})
	r.syncDefault()
}

func (r *reviewRecord) syncDefault() {
	if len(r.Reviewers) > 0 {
		r.Reviewer = r.Reviewers[0]
	}
}

func (r *reviewRecord) openBy(profile string) bool {
	for _, f := range r.Findings {
		if f.Reviewer == profile && f.Status == findingOpen {
			return true
		}
	}
	return false
}

// hack: models wrap JSON in prose or fences, so the first object carrying a
// findings array is the answer.
func parseReviewReply(text string) ([]reviewFinding, error) {
	for i := strings.IndexByte(text, '{'); i >= 0; {
		var reply struct {
			Findings *[]reviewFinding `json:"findings"`
		}
		if err := json.NewDecoder(strings.NewReader(text[i:])).Decode(&reply); err == nil && reply.Findings != nil {
			return normalizeFindings(*reply.Findings)
		}
		next := strings.IndexByte(text[i+1:], '{')
		if next < 0 {
			break
		}
		i += next + 1
	}
	return nil, fmt.Errorf("reviewer reply holds no JSON object with a findings array: %q", clip(text, 400))
}

func normalizeFindings(in []reviewFinding) ([]reviewFinding, error) {
	out := make([]reviewFinding, 0, len(in))
	for i, f := range in {
		f.Severity = strings.ToUpper(strings.TrimSpace(f.Severity))
		if severityRank(f.Severity) < 0 {
			return nil, fmt.Errorf("reviewer finding %d (%s) has severity %q, not P0 to P3", i+1, f.ID, f.Severity)
		}
		if strings.TrimSpace(f.Summary) == "" {
			return nil, fmt.Errorf("reviewer finding %d (%s) has no summary", i+1, f.ID)
		}
		switch f.Status = strings.ToLower(strings.TrimSpace(f.Status)); f.Status {
		case findingFixed:
		default:
			f.Status = findingOpen
		}
		f.SkipReason = ""
		out = append(out, f)
	}
	return out, nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// safety: earlier findings the reviewer leaves out stay as they were, so
// silence never clears a blocking finding.
func mergeFindings(prior, reply []reviewFinding, profile, prefix string) []reviewFinding {
	out := append([]reviewFinding(nil), prior...)
	index := map[string]int{}
	used := map[string]bool{}
	for i, f := range out {
		used[f.ID] = true
		if f.Reviewer == profile {
			index[f.ID] = i
		}
	}
	next := 1
	for _, f := range reply {
		f.Reviewer = profile
		if i, ok := index[f.ID]; ok && f.ID != "" {
			old := out[i]
			if old.Status == findingSkipped && f.Status == findingOpen {
				f.Status, f.SkipReason = findingSkipped, old.SkipReason
			}
			if f.Status == findingFixed {
				f.Summary = old.Summary
			}
			out[i] = f
			delete(index, f.ID)
			continue
		}
		if f.ID == "" || used[f.ID] || !strings.HasPrefix(f.ID, prefix) {
			for used[prefix+strconv.Itoa(next)] {
				next++
			}
			f.ID = prefix + strconv.Itoa(next)
		}
		used[f.ID] = true
		out = append(out, f)
	}
	return out
}

func parseSkips(raw string) (map[string]string, error) {
	out := map[string]string{}
	for _, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id, reason, ok := strings.Cut(entry, "=")
		id, reason = strings.TrimSpace(id), strings.TrimSpace(reason)
		if !ok || id == "" || reason == "" {
			return nil, fmt.Errorf("skip %q: write it as <finding id>=<reason>", entry)
		}
		out[id] = reason
	}
	return out, nil
}

// safety: an earlier skip reopens once the reviewer makes it blocking, so an
// author skip never outlives a raised severity.
func applySkips(p reviewPolicy, findings []reviewFinding, skips map[string]string) []string {
	var notes []string
	found := map[string]bool{}
	for i := range findings {
		f := &findings[i]
		reason, asked := skips[f.ID]
		found[f.ID] = found[f.ID] || asked
		switch {
		case f.Status == findingSkipped && !p.skippable(*f):
			f.Status, f.SkipReason = findingOpen, ""
			notes = append(notes, fmt.Sprintf("%s reopened: it is %s and blocks, so only the reviewer clears it", f.ID, f.Severity))
		case !asked || f.Status == findingFixed:
		case !p.skippable(*f):
			notes = append(notes, fmt.Sprintf("skip of %s refused: it is %s and blocks, so only the reviewer clears it", f.ID, f.Severity))
		default:
			f.Status, f.SkipReason = findingSkipped, reason
		}
	}
	var missing []string
	for id := range skips {
		if !found[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	for _, id := range missing {
		notes = append(notes, fmt.Sprintf("skip of %s ignored: the record has no such finding", id))
	}
	return notes
}

func (r *reviewRecord) settle(p reviewPolicy) {
	r.BlockingOpen = 0
	for _, f := range r.Findings {
		if f.Status == findingOpen && p.blocks(f) {
			r.BlockingOpen++
		}
	}
	r.Result = resultPass
	if r.BlockingOpen > 0 {
		r.Result = resultBlock
	}
}

func reviewRecordPath(dir, branch string) string {
	name := strings.NewReplacer("/", "__", "\\", "__", "..", "_").Replace(branch)
	return filepath.Join(dir, name+".json")
}

func loadReviewRecord(file string) (*reviewRecord, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec reviewRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("read review record %s: %w", file, err)
	}
	return &rec, nil
}

func saveReviewRecord(file string, rec reviewRecord) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func reviewSummary(rec reviewRecord, file string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Review: %s\n\n", rec.Result)
	fmt.Fprintf(&b, "Tree `%s` on `%s` against `%s`. Objective: %s", short(rec.Tree), rec.Branch, short(rec.Base), rec.Objective.Source)
	if rec.Objective.Ref != "" {
		fmt.Fprintf(&b, " (%s)", rec.Objective.Ref)
	}
	fmt.Fprintf(&b, ". %d blocking finding(s) open.\n\n", rec.BlockingOpen)
	if len(rec.Findings) > 0 {
		b.WriteString("| ID | Severity | Status | Rule | File | Summary |\n| --- | --- | --- | --- | --- | --- |\n")
		for _, f := range rec.Findings {
			status := f.Status
			if f.SkipReason != "" {
				status += ": " + f.SkipReason
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", f.ID, f.Severity, status, f.Rule, f.File, strings.ReplaceAll(f.Summary, "|", "\\|"))
		}
		b.WriteString("\n")
	}
	for _, n := range rec.Notes {
		fmt.Fprintf(&b, "- %s\n", n)
	}
	if file != "" {
		fmt.Fprintf(&b, "\nRecord: `%s`\n", file)
	}
	return b.String()
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
