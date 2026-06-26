package pipelinegen

import (
	"context"
	"time"
)

// SpecResult is the scored outcome of one corpus spec.
type SpecResult struct {
	Name       string        `json:"name"`
	Shape      string        `json:"shape"`
	Expect     Expectation   `json:"expect"`
	Checks     []CheckResult `json:"checks,omitempty"`
	GenError   string        `json:"gen_error,omitempty"`
	Passed     bool          `json:"passed"`  // cleared every check
	Matched    bool          `json:"matched"` // Passed agrees with Expect
	GenMS      int64         `json:"gen_ms"`  // generation wall time
	TotalMS    int64         `json:"total_ms"`
	genLatency time.Duration
	latency    time.Duration
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
	// TotalMS is the wall-clock time of the whole run.
	TotalMS int64 `json:"total_ms"`
}

// Run generates and scores every spec and aggregates the report. The
// per-spec generation and scoring are sequential so latency numbers are
// uncontended wall-clock, which is the figure the eval reports.
func Run(ctx context.Context, specs []Spec, gen Generator, scorer Scorer) Report {
	report := Report{Generator: gen.Label()}
	start := time.Now()
	for _, spec := range specs {
		report.Results = append(report.Results, scoreSpec(ctx, spec, gen, scorer))
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
	return report
}

func scoreSpec(ctx context.Context, spec Spec, gen Generator, scorer Scorer) SpecResult {
	res := SpecResult{Name: spec.Name, Shape: spec.Shape, Expect: spec.Expect}
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

	checks, scoreErr := scorer.Score(ctx, spec, source)
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
