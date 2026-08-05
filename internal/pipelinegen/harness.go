package pipelinegen

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SpecResult is the scored outcome of one corpus spec.
type SpecResult struct {
	Name   string      `json:"name"`
	Shape  string      `json:"shape"`
	Expect Expectation `json:"expect"`
	// Attempt is the 1-based repetition index this result came from.
	// Always 1 unless RunOptions.Repeat asked for more.
	Attempt  int           `json:"attempt"`
	Checks   []CheckResult `json:"checks,omitempty"`
	GenError string        `json:"gen_error,omitempty"`
	// SourcePath is where this generation's source was written, when
	// RunOptions.SaveDir asked for it. A live failure is otherwise only
	// visible as truncated tool output, which is not enough to tell a
	// hallucinated symbol from a genuine doc gap.
	SourcePath string `json:"source_path,omitempty"`
	// Revisions is how many feedback rounds this attempt consumed
	// before it landed. Zero means it cleared the bar first try.
	Revisions  int   `json:"revisions,omitempty"`
	Passed     bool  `json:"passed"`  // cleared every check
	Matched    bool  `json:"matched"` // Passed agrees with Expect
	GenMS      int64 `json:"gen_ms"`  // generation wall time
	TotalMS    int64 `json:"total_ms"`
	genLatency time.Duration
	latency    time.Duration
}

// SpecStat rolls every attempt at one spec into a single row. With a
// stochastic generator the per-attempt lines are individually
// uninformative; this is the figure to read.
type SpecStat struct {
	Name     string      `json:"name"`
	Shape    string      `json:"shape"`
	Expect   Expectation `json:"expect"`
	Attempts int         `json:"attempts"`
	// Matched counts attempts that agreed with Expect.
	Matched int `json:"matched"`
	// Passed counts attempts that cleared every check.
	Passed int `json:"passed"`
	// FirstTry counts attempts that cleared the bar with no revision
	// round. Passed minus FirstTry is the work the feedback loop did.
	FirstTry int `json:"first_try"`
	// MatchRate is Matched/Attempts: the share of attempts that landed
	// on the expected side of the bar.
	MatchRate float64 `json:"match_rate"`
}

// RunOptions tunes a harness run.
type RunOptions struct {
	// Repeat is how many times each spec is generated and scored.
	// Zero and one both mean a single attempt. A live cold author is
	// stochastic -- the same spec can compile on one attempt and not
	// the next -- so a single sample per spec is a coin flip, not a
	// measurement. Repeat > 1 is what makes the pass rate a number.
	Repeat int
	// SaveDir, when non-empty, is a directory every generation's source
	// is written to as <spec>-<attempt>.go. Passing generations are
	// saved too, since diffing a passing attempt against a failing one
	// is the fastest way to characterize a flaky spec.
	SaveDir string
	// Revise is how many feedback rounds a failing generation gets:
	// the oracle output goes back to the generator and the fix is
	// re-scored. Zero keeps the run one-shot. Ignored when the
	// generator does not implement Reviser.
	Revise int
}

func (o RunOptions) repeat() int {
	if o.Repeat < 1 {
		return 1
	}
	return o.Repeat
}

// Report is the aggregate result of a harness run.
type Report struct {
	Generator string       `json:"generator"`
	Results   []SpecResult `json:"results"`
	Total     int          `json:"total"`
	// PassExpected is the number of specs marked expect=pass.
	PassExpected int `json:"pass_expected"`
	// Passed is how many expect=pass specs actually cleared the bar.
	Passed int `json:"passed"`
	// Matched is how many specs (pass and fail) agreed with expectation;
	// Matched < Total means the corpus caught a regression.
	Matched int `json:"matched"`
	// PassRate is Passed / PassExpected: the headline generation
	// success rate over the idiomatic specs.
	PassRate float64 `json:"pass_rate"`
	// Repeat is how many attempts each spec got.
	Repeat int `json:"repeat"`
	// Stats rolls the attempts up per spec. With Repeat > 1 this is the
	// readable view; Results is the raw per-attempt log behind it.
	Stats []SpecStat `json:"stats,omitempty"`
	// TotalMS is the wall-clock time of the whole run.
	TotalMS int64 `json:"total_ms"`
}

// Run generates and scores every spec once and aggregates the report.
// It is RunWith with default options; see RunWith for repetition and
// source capture.
func Run(ctx context.Context, specs []Spec, gen Generator, scorer Scorer) Report {
	return RunWith(ctx, specs, gen, scorer, RunOptions{})
}

// RunWith generates and scores every spec opts.Repeat times and
// aggregates the report. The per-spec generation and scoring are
// sequential so latency numbers are uncontended wall-clock, which is the
// figure the eval reports.
func RunWith(ctx context.Context, specs []Spec, gen Generator, scorer Scorer, opts RunOptions) Report {
	repeat := opts.repeat()
	report := Report{Generator: gen.Label(), Repeat: repeat}
	start := time.Now()
	for _, spec := range specs {
		for attempt := 1; attempt <= repeat; attempt++ {
			report.Results = append(report.Results, scoreSpec(ctx, spec, gen, scorer, attempt, opts))
		}
	}
	report.TotalMS = time.Since(start).Milliseconds()

	report.Total = len(report.Results)
	for _, r := range report.Results {
		if r.Expect == ExpectPass {
			report.PassExpected++
			if r.Passed {
				report.Passed++
			}
		}
		if r.Matched {
			report.Matched++
		}
	}
	if report.PassExpected > 0 {
		report.PassRate = float64(report.Passed) / float64(report.PassExpected)
	}
	report.Stats = rollUp(report.Results)
	return report
}

// rollUp collapses the per-attempt results into one row per spec,
// preserving corpus order.
func rollUp(results []SpecResult) []SpecStat {
	var stats []SpecStat
	index := map[string]int{}
	for _, r := range results {
		i, seen := index[r.Name]
		if !seen {
			i = len(stats)
			index[r.Name] = i
			stats = append(stats, SpecStat{Name: r.Name, Shape: r.Shape, Expect: r.Expect})
		}
		stats[i].Attempts++
		if r.Matched {
			stats[i].Matched++
		}
		if r.Passed {
			stats[i].Passed++
			if r.Revisions == 0 {
				stats[i].FirstTry++
			}
		}
	}
	for i := range stats {
		if stats[i].Attempts > 0 {
			stats[i].MatchRate = float64(stats[i].Matched) / float64(stats[i].Attempts)
		}
	}
	return stats
}

// scoreSpec generates one attempt at spec, scores it, and optionally
// spends opts.Revise feedback rounds trying to rescue a rejected
// generation.
//
// Revision is confined to expect=pass specs. An expect=fail spec is
// fixture-only and measures the linter rather than the author, so
// handing it the linter's complaint would ask it to stop reproducing
// the anti-pattern it exists to encode.
//
// A SaveDir write failure is reported through GenError rather than
// failing the attempt: it says nothing about the generation being
// measured, but a report pointing at source that was never written
// would be worse than a noisy one.
func scoreSpec(ctx context.Context, spec Spec, gen Generator, scorer Scorer, attempt int, opts RunOptions) SpecResult {
	res := SpecResult{Name: spec.Name, Shape: spec.Shape, Expect: spec.Expect, Attempt: attempt}
	specStart := time.Now()

	genStart := time.Now()
	source, err := gen.Generate(ctx, spec)
	res.genLatency = time.Since(genStart)
	res.GenMS = res.genLatency.Milliseconds()
	if err != nil {
		res.GenError = err.Error()
		res.Passed = false
		res.Matched = spec.Expect == ExpectFail
		res.latency = time.Since(specStart)
		res.TotalMS = res.latency.Milliseconds()
		return res
	}

	saveAttempt := func(round int, src string) {
		if opts.SaveDir == "" {
			return
		}
		path, saveErr := saveSource(opts.SaveDir, spec.Name, attempt, round, src)
		if saveErr != nil {
			res.GenError = "save source: " + saveErr.Error()
		}
		res.SourcePath = path
	}
	saveAttempt(0, source)

	checks, scoreErr := scorer.Score(ctx, spec, source)

	reviser, canRevise := gen.(Reviser)
	for round := 1; canRevise && spec.Expect == ExpectPass && round <= opts.Revise; round++ {
		if scoreErr == nil && allOK(checks) {
			break
		}
		revised, rerr := reviser.Revise(ctx, spec, source, feedback(checks, scoreErr))
		if rerr != nil {
			res.GenError = fmt.Sprintf("revise round %d: %v", round, rerr)
			break
		}
		source = revised
		res.Revisions = round
		saveAttempt(round, source)
		checks, scoreErr = scorer.Score(ctx, spec, source)
	}

	res.Checks = checks
	res.Passed = scoreErr == nil && allOK(checks)
	if scoreErr != nil {
		res.GenError = "score: " + scoreErr.Error()
	}
	res.Matched = res.Passed == (spec.Expect == ExpectPass)
	res.latency = time.Since(specStart)
	res.TotalMS = res.latency.Milliseconds()
	return res
}

// feedback renders the failing checks as the text a reviser reads: the
// oracle's own output, which is what a human author would be looking at.
func feedback(checks []CheckResult, scoreErr error) string {
	var b strings.Builder
	if scoreErr != nil {
		fmt.Fprintf(&b, "harness error: %v\n", scoreErr)
	}
	for _, c := range checks {
		if c.OK {
			continue
		}
		fmt.Fprintf(&b, "--- %s failed ---\n%s\n", c.Name, c.Detail)
	}
	if b.Len() == 0 {
		return "the generation was rejected but no check reported detail"
	}
	return b.String()
}

// saveSource writes one generation's source to dir as
// <spec>-<attempt>.go, suffixed -rN for revision round N, and returns
// the path it wrote. Keeping each round lets a reader see what the
// feedback actually changed.
func saveSource(dir, spec string, attempt, round int, source string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%d.go", spec, attempt)
	if round > 0 {
		name = fmt.Sprintf("%s-%d-r%d.go", spec, attempt, round)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func allOK(checks []CheckResult) bool {
	if len(checks) == 0 {
		return false
	}
	for _, c := range checks {
		if !c.OK {
			return false
		}
	}
	return true
}
