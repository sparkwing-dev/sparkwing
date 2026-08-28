package pipelinegen

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type SpecResult struct {
	Name   string      `json:"name"`
	Shape  string      `json:"shape"`
	Expect Expectation `json:"expect"`

	Attempt  int           `json:"attempt"`
	Checks   []CheckResult `json:"checks,omitempty"`
	GenError string        `json:"gen_error,omitempty"`

	SourcePath string `json:"source_path,omitempty"`

	Revisions  int   `json:"revisions,omitempty"`
	Passed     bool  `json:"passed"`
	Matched    bool  `json:"matched"`
	GenMS      int64 `json:"gen_ms"`
	TotalMS    int64 `json:"total_ms"`
	genLatency time.Duration
	latency    time.Duration
}

type SpecStat struct {
	Name     string      `json:"name"`
	Shape    string      `json:"shape"`
	Expect   Expectation `json:"expect"`
	Attempts int         `json:"attempts"`

	Matched int `json:"matched"`

	Passed int `json:"passed"`

	FirstTry int `json:"first_try"`

	MatchRate float64 `json:"match_rate"`
}

type RunOptions struct {
	Repeat int

	SaveDir string

	Revise int
}

func (o RunOptions) repeat() int {
	if o.Repeat < 1 {
		return 1
	}
	return o.Repeat
}

type Report struct {
	Generator string       `json:"generator"`
	Results   []SpecResult `json:"results"`
	Total     int          `json:"total"`

	PassExpected int `json:"pass_expected"`

	Passed int `json:"passed"`

	Matched int `json:"matched"`

	PassRate float64 `json:"pass_rate"`

	Repeat int `json:"repeat"`

	Stats []SpecStat `json:"stats,omitempty"`

	TotalMS int64 `json:"total_ms"`
}

func Run(ctx context.Context, specs []Spec, gen Generator, scorer Scorer) Report {
	return RunWith(ctx, specs, gen, scorer, RunOptions{})
}

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
