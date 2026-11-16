package controller

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

const (
	// safety: GitHub refuses a check run whose summary or text exceeds this many characters.
	githubCheckFieldRunes = 65535
	// perf: keeps one delivery to a handful of writes inside the reporter's timeout.
	githubCheckAnnotationCap = 200
	// safety: GitHub refuses an annotation message longer than this.
	githubAnnotationMessageBytes = 64 << 10
	githubAnnotationTitleRunes   = 255
	githubCheckErrorRunes        = 8000
	githubCheckErrorLineRunes    = 200
)

type githubCheckResult struct {
	label       string
	failed      bool
	err         string
	summary     string
	annotations []nodewire.Annotation
}

// safety: steps of superseded attempts are dropped, so a retried job reports only its latest attempt.
func githubCheckResults(nodes []*store.Node, steps []*store.NodeStep) []githubCheckResult {
	byJob := map[string][]*store.NodeStep{}
	for _, step := range steps {
		byJob[step.NodeID] = append(byJob[step.NodeID], step)
	}
	var out []githubCheckResult
	for _, n := range nodes {
		out = append(out, githubCheckResult{
			label: n.NodeID, failed: n.Outcome == string(runstate.NodeFailed), err: n.Error,
			summary: n.Summary, annotations: n.Annotations,
		})
		for _, step := range byJob[n.NodeID] {
			if step.Attempt != int64(n.AttemptsConsumed) {
				continue
			}
			out = append(out, githubCheckResult{
				label: n.NodeID + " / " + step.StepID, failed: step.Status == "failed", err: step.Error,
				summary: step.Summary, annotations: step.Annotations,
			})
		}
	}
	return out
}

func githubCheckDetails(run *store.Run, nodes []*store.Node, steps []*store.NodeStep, conclusion, link string) (*githubapp.CheckRunOutput, []githubapp.CheckRunAnnotation) {
	results := githubCheckResults(nodes, steps)
	failedJobs := 0
	for _, n := range nodes {
		if n.Outcome == string(runstate.NodeFailed) {
			failedJobs++
		}
	}
	title := githubCheckTitle(conclusion)
	if failedJobs > 0 {
		noun := "jobs"
		if len(nodes) == 1 {
			noun = "job"
		}
		title += fmt.Sprintf(": %d of %d %s failed", failedJobs, len(nodes), noun)
	}

	var annotations []githubapp.CheckRunAnnotation
	withPath := 0
	for _, result := range results {
		for _, a := range result.annotations {
			if a.File == "" {
				continue
			}
			withPath++
			if len(annotations) < githubCheckAnnotationCap {
				annotations = append(annotations, githubCheckAnnotation(result.label, a))
			}
		}
	}

	summary := newGitHubCheckField(link)
	summary.add(githubCheckSummary(run, nodes, conclusion, link))
	if more := withPath - len(annotations); more > 0 {
		summary.add(fmt.Sprintf("\n\n%d more annotations on the run page.", more))
	}
	var failures strings.Builder
	for _, result := range results {
		if result.failed && result.err != "" {
			line, _, _ := strings.Cut(result.err, "\n")
			fmt.Fprintf(&failures, "\n- **%s**: %s", result.label, truncateRunes(line, githubCheckErrorLineRunes))
		}
	}
	if failures.Len() > 0 {
		summary.add("\n\n## Failed jobs\n" + failures.String())
	}
	for _, result := range results {
		if result.summary != "" {
			summary.add("\n\n### " + result.label + "\n\n" + closeFences(result.summary))
		}
	}

	text := newGitHubCheckField(link)
	if run != nil && run.Error != "" {
		text.add(githubCheckFailure("Run", run.Error))
	}
	for _, result := range results {
		if result.failed && result.err != "" {
			text.add(githubCheckFailure(result.label, result.err))
		}
	}
	return &githubapp.CheckRunOutput{Title: title, Summary: summary.String(), Text: strings.TrimPrefix(text.String(), "\n\n")}, annotations
}

func githubCheckFailure(label, err string) string {
	if cut := truncateRunes(err, githubCheckErrorRunes); cut != err {
		err = cut + "\n…"
	}
	fence := strings.Repeat("`", max(3, longestRun(err, '`')+1))
	return "\n\n### " + label + "\n\n" + fence + "\n" + err + "\n" + fence
}

func githubCheckAnnotation(label string, a nodewire.Annotation) githubapp.CheckRunAnnotation {
	out := githubapp.CheckRunAnnotation{
		Path: strings.TrimPrefix(a.File, "./"), StartLine: max(a.Line, 1), Title: truncateRunes(label, githubAnnotationTitleRunes),
		Message: truncateBytes(a.Message, githubAnnotationMessageBytes),
	}
	out.EndLine = max(a.EndLine, out.StartLine)
	if out.StartLine == out.EndLine && a.Column > 0 {
		out.StartColumn, out.EndColumn = a.Column, a.Column
	}
	switch a.Level {
	case "error":
		out.Level = "failure"
	case "warn":
		out.Level = "warning"
	default:
		out.Level = "notice"
	}
	if out.Message == "" {
		out.Message = label
	}
	return out
}

// safety: a piece cut at the limit gets its open code fence closed, so the note after it still renders.
type githubCheckField struct {
	b     strings.Builder
	runes int
	note  string
	full  bool
}

func newGitHubCheckField(link string) *githubCheckField {
	note := "\n\n… truncated; the full text is on the run page."
	if link != "" {
		note = "\n\n… truncated; the full text is on the [run page](" + link + ")."
	}
	return &githubCheckField{note: note}
}

func (f *githubCheckField) add(piece string) {
	if f.full {
		return
	}
	room := githubCheckFieldRunes - utf8.RuneCountInString(f.note)
	if n := utf8.RuneCountInString(piece); f.runes+n <= room {
		f.b.WriteString(piece)
		f.runes += n
		return
	}
	cut := truncateRunes(piece, max(room-f.runes, 0))
	for {
		closing := ""
		if fence := openFence(cut); fence != "" {
			closing = "\n" + fence
		}
		over := f.runes + utf8.RuneCountInString(cut) + utf8.RuneCountInString(closing) - room
		if over <= 0 {
			f.b.WriteString(cut + closing)
			break
		}
		cut = truncateRunes(cut, max(utf8.RuneCountInString(cut)-over, 0))
	}
	f.b.WriteString(f.note)
	f.full = true
}

func (f *githubCheckField) String() string { return f.b.String() }

// safety: an author's unclosed fence would otherwise swallow every later section.
func closeFences(doc string) string {
	if fence := openFence(doc); fence != "" {
		return doc + "\n" + fence
	}
	return doc
}

func openFence(doc string) string {
	open := ""
	for line := range strings.SplitSeq(doc, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) > 3 || trimmed == "" || (trimmed[0] != '`' && trimmed[0] != '~') {
			continue
		}
		run := longestPrefix(trimmed, trimmed[0])
		switch {
		case run < 3:
		case open == "":
			if trimmed[0] == '`' && strings.ContainsRune(trimmed[run:], '`') {
				continue
			}
			open = trimmed[:run]
		case trimmed[0] == open[0] && run >= len(open) && strings.TrimSpace(trimmed[run:]) == "":
			open = ""
		}
	}
	return open
}

func longestPrefix(s string, c byte) int {
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	return n
}

func longestRun(s string, c byte) int {
	best, cur := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			cur++
			best = max(best, cur)
		} else {
			cur = 0
		}
	}
	return best
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for range n {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}

func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
