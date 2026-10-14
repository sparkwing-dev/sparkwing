package sdkserve_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

const contextProbePipeline = "context-probe"

var contextProbeOrder = []string{
	"Pipeline.Plan",
	"JobNode.SkipIf",
	"CacheKeyFn",
	"Job body step",
	"Verify",
	"BeforeRun",
	"AfterRun",
}

type contextSighting struct {
	node, step  string
	sideEffects bool
}

var (
	seenMu sync.Mutex
	seen   = map[string]contextSighting{}
)

func probeContext(ctx context.Context, site string) {
	seenMu.Lock()
	defer seenMu.Unlock()
	seen[site] = contextSighting{
		node:        sparkwing.NodeFromContext(ctx),
		step:        sparkwing.StepFromContext(ctx),
		sideEffects: guardAllows(ctx),
	}
}

func guardAllows(ctx context.Context) (ok bool) {
	defer func() { ok = recover() == nil }()
	planguard.Guard(ctx, "probe")
	return true
}

type contextProbe struct{ sparkwing.Base }

func (contextProbe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	probeContext(ctx, "Pipeline.Plan")
	sparkwing.Job(plan, "probe", func(ctx context.Context) error {
		probeContext(ctx, "Job body step")
		return nil
	}).
		SkipIf(func(ctx context.Context) bool {
			probeContext(ctx, "JobNode.SkipIf")
			return false
		}).
		Memoize(func(ctx context.Context) (sparkwing.CacheKey, error) {
			probeContext(ctx, "CacheKeyFn")
			return sparkwing.NoCache, nil
		}).
		Verify(func(ctx context.Context) error {
			probeContext(ctx, "Verify")
			return nil
		}).
		BeforeRun(func(ctx context.Context) error {
			probeContext(ctx, "BeforeRun")
			return nil
		}).
		AfterRun(func(ctx context.Context, _ error) {
			probeContext(ctx, "AfterRun")
		})
	return nil
}

func init() {
	sparkwing.Register[sparkwing.NoInputs](contextProbePipeline, func() sparkwing.Pipeline[sparkwing.NoInputs] { return contextProbe{} })
}

// safety: drives each callback through the requests a host sends, in the
// host's order, so the table describes what pipeline code sees in a run.
func measureContexts(t *testing.T) string {
	t.Helper()
	s := serve(t, nil)
	if r := s.call(nodeproto.RequestOld{Op: nodeproto.OpPlanOld, Pipeline: contextProbePipeline, Args: map[string]string{}, Run: &nodeproto.RunContextOld{RunID: "probe-run"}}); !r.OK {
		t.Fatal(r.Error)
	}
	eval := func(kind string, input json.RawMessage) {
		t.Helper()
		if r := s.call(nodeproto.RequestOld{Op: nodeproto.OpEvalOld, Closure: "probe/" + kind + "/0", Input: input}); !r.OK {
			t.Fatalf("%s: %+v", kind, r.Error)
		}
	}
	eval("skip_if", nil)
	eval("cache_key", nil)
	eval("before_run", nil)
	if res := s.run(nodeproto.OpRunStepOld, "probe", "run"); res.Outcome != nodeproto.OutcomeSuccess {
		t.Fatalf("body: %+v", res)
	}
	eval("verify", nil)
	eval("after_run", json.RawMessage(`{"outcome":"success"}`))

	seenMu.Lock()
	defer seenMu.Unlock()
	yesNo := map[bool]string{true: "yes", false: "no"}
	var b strings.Builder
	b.WriteString("| Callback | Side effects | Node id | Step id |\n|---|---|---|---|\n")
	for _, site := range contextProbeOrder {
		c, ok := seen[site]
		if !ok {
			t.Fatalf("%s never ran, so the run proves nothing about it", site)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", site, yesNo[c.sideEffects], yesNo[c.node != ""], yesNo[c.step != ""])
	}
	return b.String()
}

func TestTheDocumentedContextTableIsWhatAHostedRunProduces(t *testing.T) {
	measured := measureContexts(t)
	path := filepath.Join("..", "..", "docs", "sdk.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const begin, end = "<!-- BEGIN measured-context-table -->", "<!-- END measured-context-table -->"
	doc := strings.ReplaceAll(string(raw), "\r\n", "\n")
	i, j := strings.Index(doc, begin), strings.Index(doc, end)
	if i < 0 || j < i {
		t.Fatalf("docs/sdk.md carries no %s .. %s block", begin, end)
	}
	if published := strings.TrimSpace(doc[i+len(begin) : j]); published != strings.TrimSpace(measured) {
		t.Fatalf("docs/sdk.md disagrees with a hosted run.\n\npublished:\n%s\n\nmeasured:\n%s", published, measured)
	}
}
