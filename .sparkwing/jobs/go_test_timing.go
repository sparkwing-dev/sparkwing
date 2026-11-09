package jobs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// perf: go test -json is the only form that times every test, so the gate's
// test steps run it and rebuild the plain transcript from the events. The
// summary answers which packages and tests made the step slow.
const goTestTimingTop = 10

type goPackageTiming struct {
	pkg     string
	elapsed float64
	cached  bool
	failed  bool
}

type goTestTiming struct {
	pkg     string
	test    string
	elapsed float64
}

type goTestTimings struct {
	mu       sync.Mutex
	packages []goPackageTiming
	tests    []goTestTiming
}

type goTestEvent struct {
	Action     string
	Package    string
	ImportPath string
	Test       string
	Output     string
	Elapsed    float64
}

var goTestCommand = regexp.MustCompile(`(^|[\s;&|(])go test `)

func withGoTestJSON(script string) (string, bool) {
	loc := goTestCommand.FindStringSubmatchIndex(script)
	if loc == nil {
		return script, false
	}
	at := loc[1]
	return script[:at] + "-json " + script[at:], true
}

// safety: the script's go test is its last command, so the appended redirect
// captures only the events and leaves stderr streaming. A failure returns an
// ExecError whose Stdout is the plain transcript, which failedTestSummaries reads.
func (t *goTestTimings) run(ctx context.Context, script string) error {
	jsonScript, ok := withGoTestJSON(script)
	if !ok {
		_, err := sparkwing.Bash(ctx, script).Env("GOWORK", "auto").Run()
		return err
	}
	f, err := os.CreateTemp("", "sparkwing-go-test-*.jsonl")
	if err != nil {
		return fmt.Errorf("create the go test event file: %w", err)
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		return fmt.Errorf("close the go test event file: %w", err)
	}
	defer func() {
		if err := os.Remove(path); err != nil {
			sparkwing.Warn(ctx, "go test timing: remove the event file: %v", err)
		}
	}()

	tr := newGoTestTranscript(func(line string) { sparkwing.Info(ctx, "%s", line) })
	done := make(chan struct{})
	followed := make(chan error, 1)
	go func() { followed <- followLines(path, done, tr.add) }()
	_, runErr := sparkwing.Bash(ctx, jsonScript+" > "+shellQuote(path)).Env("GOWORK", "auto").Run()
	close(done)
	followErr := <-followed
	t.merge(tr)
	if followErr != nil {
		sparkwing.Warn(ctx, "go test timing: read the event file: %v", followErr)
	}
	var execErr *sparkwing.ExecError
	if runErr != nil && errors.As(runErr, &execErr) {
		withTranscript := *execErr
		withTranscript.Stdout = tr.plain()
		return &withTranscript
	}
	return runErr
}

func runModuleCommand(ctx context.Context, script string, timings *goTestTimings) error {
	if timings == nil {
		_, err := sparkwing.Bash(ctx, script).Env("GOWORK", "auto").Run()
		return err
	}
	return timings.run(ctx, script)
}

func (t *goTestTimings) merge(tr *goTestTranscript) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.packages = append(t.packages, tr.packages...)
	t.tests = append(t.tests, tr.tests...)
}

func (t *goTestTimings) report(ctx context.Context, start time.Time) {
	if md := t.summary(time.Since(start)); md != "" {
		sparkwing.Summary(ctx, md)
	}
}

func (t *goTestTimings) summary(wall time.Duration) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.packages) == 0 {
		return ""
	}
	var ran []goPackageTiming
	cached, failed := 0, 0
	sum := 0.0
	for _, p := range t.packages {
		if p.failed {
			failed++
		}
		if p.cached {
			cached++
			continue
		}
		ran = append(ran, p)
		sum += p.elapsed
	}
	sort.SliceStable(ran, func(i, j int) bool { return ran[i].elapsed > ran[j].elapsed })
	tests := append([]goTestTiming(nil), t.tests...)
	sort.SliceStable(tests, func(i, j int) bool { return tests[i].elapsed > tests[j].elapsed })

	var b strings.Builder
	fmt.Fprintf(&b, "**go test timing**: %d package(s) in %s wall, %d cached, %d ran for %s summed",
		len(t.packages), wall.Round(time.Second), cached, len(ran), seconds(sum))
	if failed > 0 {
		fmt.Fprintf(&b, ", %d failed", failed)
	}
	b.WriteString("\n\n")
	if len(ran) > 0 {
		b.WriteString("Slowest packages\n\n| package | time |\n| --- | ---: |\n")
		for _, p := range ran[:min(goTestTimingTop, len(ran))] {
			mark := ""
			if p.failed {
				mark = " (failed)"
			}
			fmt.Fprintf(&b, "| `%s`%s | %s |\n", p.pkg, mark, seconds(p.elapsed))
		}
		b.WriteString("\n")
	}
	if len(tests) > 0 {
		b.WriteString("Slowest tests\n\n| test | package | time |\n| --- | --- | ---: |\n")
		for _, tt := range tests[:min(goTestTimingTop, len(tests))] {
			fmt.Fprintf(&b, "| `%s` | `%s` | %s |\n", tt.test, tt.pkg, seconds(tt.elapsed))
		}
	}
	return b.String()
}

func seconds(s float64) string {
	return (time.Duration(s * float64(time.Second))).Round(10 * time.Millisecond).String()
}

// safety: the transcript keeps what plain go test prints (package results,
// build output, failed tests' output) so the log and failure summaries read
// as they did before the step switched to -json.
type goTestTranscript struct {
	emit     func(string)
	lines    []string
	buffered map[string][]string
	cached   map[string]bool
	pending  map[string][]goTestTiming
	packages []goPackageTiming
	tests    []goTestTiming
}

func newGoTestTranscript(emit func(string)) *goTestTranscript {
	return &goTestTranscript{emit: emit, buffered: map[string][]string{}, cached: map[string]bool{}, pending: map[string][]goTestTiming{}}
}

func (tr *goTestTranscript) print(line string) {
	line = strings.TrimRight(line, "\r\n")
	tr.lines = append(tr.lines, line)
	if tr.emit != nil {
		tr.emit(line)
	}
}

func (tr *goTestTranscript) plain() string { return strings.Join(tr.lines, "\n") + "\n" }

func (tr *goTestTranscript) add(raw []byte) {
	raw = bytes.TrimRight(raw, "\r\n")
	if len(raw) == 0 {
		return
	}
	var ev goTestEvent
	if raw[0] != '{' || json.Unmarshal(raw, &ev) != nil {
		tr.print(string(raw))
		return
	}
	switch ev.Action {
	case "build-output":
		tr.print(ev.Output)
	case "output":
		tr.output(ev)
	case "pass", "fail", "skip":
		tr.result(ev)
	}
}

func (tr *goTestTranscript) output(ev goTestEvent) {
	if ev.Test != "" {
		if !strings.HasPrefix(ev.Output, "=== ") {
			key := ev.Package + "\x00" + ev.Test
			tr.buffered[key] = append(tr.buffered[key], ev.Output)
		}
		return
	}
	trimmed := strings.TrimSpace(ev.Output)
	if trimmed == "PASS" || strings.HasPrefix(ev.Output, "=== ") {
		return
	}
	if strings.HasPrefix(ev.Output, "ok  \t") && strings.Contains(ev.Output, "(cached)") {
		tr.cached[ev.Package] = true
	}
	tr.print(ev.Output)
}

func (tr *goTestTranscript) result(ev goTestEvent) {
	if ev.Test == "" {
		tr.packageResult(ev)
		return
	}
	key := ev.Package + "\x00" + ev.Test
	if ev.Action == "fail" {
		for _, line := range tr.buffered[key] {
			tr.print(line)
		}
	}
	delete(tr.buffered, key)
	if !strings.Contains(ev.Test, "/") {
		tr.pending[ev.Package] = append(tr.pending[ev.Package], goTestTiming{pkg: ev.Package, test: ev.Test, elapsed: ev.Elapsed})
	}
}

// safety: a cached package replays its tests before the result line that says
// so, and a timed-out test never reports its own result, so test timings and
// unreported output settle only at the package's result.
func (tr *goTestTranscript) packageResult(ev goTestEvent) {
	prefix := ev.Package + "\x00"
	var orphans []string
	for key := range tr.buffered {
		if strings.HasPrefix(key, prefix) {
			orphans = append(orphans, key)
		}
	}
	sort.Strings(orphans)
	for _, key := range orphans {
		if ev.Action == "fail" {
			for _, line := range tr.buffered[key] {
				tr.print(line)
			}
		}
		delete(tr.buffered, key)
	}
	if !tr.cached[ev.Package] {
		tr.tests = append(tr.tests, tr.pending[ev.Package]...)
	}
	delete(tr.pending, ev.Package)
	if ev.Package != "" && (ev.Action != "skip" || ev.Elapsed > 0) {
		tr.packages = append(tr.packages, goPackageTiming{pkg: ev.Package, elapsed: ev.Elapsed, cached: tr.cached[ev.Package], failed: ev.Action == "fail"})
	}
}

// safety: a line is handed on only once complete, and the rest drains after
// done closes, so no event is split or lost at the end of the run.
func followLines(path string, done <-chan struct{}, line func([]byte)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(f, 64<<10)
	var partial []byte
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	finished := false
	for {
		chunk, err := r.ReadBytes('\n')
		partial = append(partial, chunk...)
		if err == nil {
			line(partial)
			partial = nil
			continue
		}
		if !errors.Is(err, io.EOF) {
			return err
		}
		if finished {
			if len(partial) > 0 {
				line(partial)
			}
			return nil
		}
		select {
		case <-done:
			finished = true
		case <-poll.C:
		}
	}
}
