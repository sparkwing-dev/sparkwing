package controller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func scrapeRegistry(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics handler status = %d, want 200", rec.Code)
	}
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	return string(body)
}

func sampleValue(t *testing.T, body, series string) float64 {
	t.Helper()
	for line := range strings.SplitSeq(body, "\n") {
		raw, ok := strings.CutPrefix(line, series+" ")
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		return v
	}
	t.Fatalf("series %q absent from the exposition", series)
	return 0
}

func seriesCount(t *testing.T, body, name string) int {
	t.Helper()
	n := 0
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, name+"{") || strings.HasPrefix(line, name+" ") {
			n++
		}
	}
	return n
}

func TestRunnerLabelSetOrdersAndDeduplicates(t *testing.T) {
	got := runnerLabelSet([]string{"zone=b", "gpu", "", "  zone=b  ", "gpu"})
	if got != "gpu,zone=b" {
		t.Errorf("runnerLabelSet = %q, want %q", got, "gpu,zone=b")
	}
}

func TestRunnerLabelSetCollapsesAnOversizedSet(t *testing.T) {
	long := strings.Repeat("wide-runner-label-", 40)
	if got := runnerLabelSet([]string{long}); got != otherLabel {
		t.Errorf("runnerLabelSet(%d bytes) = %q, want %q", len(long), got, otherLabel)
	}
}

func TestRunnerLivenessSamplerCollapsesPastTheLabelSetCap(t *testing.T) {
	sampler := &runnerLivenessSampler{report: map[string]struct{}{}}
	sets := make([][]string, 0, maxRunnerLabelSets*2)
	for i := range maxRunnerLabelSets * 2 {
		sets = append(sets, []string{"capped-runner-" + strconv.Itoa(i)})
	}
	sampler.sample(sets)

	body := scrapeRegistry(t)
	if got := seriesCount(t, body, "sparkwing_runners_live"); got > maxRunnerLabelSets+2 {
		t.Errorf("runners_live series = %d, want the cap to bound them", got)
	}
	if got := sampleValue(t, body, `sparkwing_runners_live{label_set="other"}`); got < 1 {
		t.Errorf("overflow series = %v, want the sets past the cap collapsed onto it", got)
	}
	sampler.sample(nil)
}

func TestRunnerLivenessSamplerZeroesASetThatWentAway(t *testing.T) {
	sampler := &runnerLivenessSampler{report: map[string]struct{}{}}
	sampler.sample([][]string{{"departing-runner"}})
	const series = `sparkwing_runners_live{label_set="departing-runner"}`
	if got := sampleValue(t, scrapeRegistry(t), series); got != 1 {
		t.Fatalf("live gauge = %v, want 1 after the runner reported", got)
	}
	sampler.sample(nil)
	if got := sampleValue(t, scrapeRegistry(t), series); got != 0 {
		t.Errorf("live gauge = %v, want 0 once the runner stopped polling", got)
	}
}

func TestSetQueueDepthCoversEveryState(t *testing.T) {
	setQueueDepth(map[string]int{store.QueueStateReady: 3})
	body := scrapeRegistry(t)
	if got := sampleValue(t, body, `sparkwing_queue_depth{state="ready"}`); got != 3 {
		t.Errorf("ready depth = %v, want 3", got)
	}
	for _, state := range store.QueueStates() {
		if state == store.QueueStateReady {
			continue
		}
		series := `sparkwing_queue_depth{state="` + state + `"}`
		if got := sampleValue(t, body, series); got != 0 {
			t.Errorf("%s depth = %v, want a state the sample omitted to read 0", state, got)
		}
	}
	setQueueDepth(map[string]int{})
}

func TestCredentialLabelClampsAnUnknownKind(t *testing.T) {
	for _, kind := range []string{store.TokenKindUser, store.TokenKindRunner, store.TokenKindService} {
		if got := credentialLabel(kind); got != kind {
			t.Errorf("credentialLabel(%q) = %q", kind, got)
		}
	}
	if got := credentialLabel("invented-by-a-caller"); got != otherLabel {
		t.Errorf("credentialLabel(invented) = %q, want %q", got, otherLabel)
	}
}

func TestCreditsCollectorReportsTheSampledLedger(t *testing.T) {
	t.Cleanup(func() { ledgerSnapshot.set(store.CreditLedgerTotals{}) })
	ledgerSnapshot.set(store.CreditLedgerTotals{
		BalanceMicro:     7_000_000,
		GrantedFreeMicro: 2_000_000,
		GrantedPaidMicro: 9_000_000,
		ReservedMicro:    1_200_000,
		ChargedMicro:     3_400_000,
		RefundedMicro:    600_000,
	})

	body := scrapeRegistry(t)
	for series, want := range map[string]float64{
		"sparkwing_credits_balance_micro":                               7_000_000,
		`sparkwing_credits_granted_micro_total{kind="free"}`:            2_000_000,
		`sparkwing_credits_granted_micro_total{kind="paid"}`:            9_000_000,
		`sparkwing_credits_reserved_micro_total{principal_kind="paid"}`: 1_200_000,
		`sparkwing_credits_reserved_micro_total{principal_kind="free"}`: 0,
		`sparkwing_credits_charged_micro_total{principal_kind="paid"}`:  3_400_000,
		`sparkwing_credits_refunded_micro_total{principal_kind="paid"}`: 600_000,
	} {
		if got := sampleValue(t, body, series); got != want {
			t.Errorf("%s = %v, want %v", series, got, want)
		}
	}
}

func TestReaperSamplesTheOperationalSeries(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.CreateRun(ctx, store.Run{
		ID: "run-reaper", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: "run-reaper", NodeID: "node-a", Status: "pending",
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if _, err := st.GrantCredits(ctx, store.CreditGrantFree, 4_000_000, "trial", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}

	srv := New(st, nil)
	srv.runnerPresence.record(presenceKey{tokenPrefix: "swr_reaper", name: "pool-a"},
		[]string{"zone=b"}, nil, time.Now())

	reaperCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.runReaper(reaperCtx, 5*time.Millisecond)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	want := map[string]float64{
		`sparkwing_queue_depth{state="waiting"}`:             1,
		`sparkwing_credits_granted_micro_total{kind="free"}`: 4_000_000,
		`sparkwing_runners_live{label_set="zone=b"}`:         1,
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		body := scrapeRegistry(t)
		settled := true
		for series, v := range want {
			if sampleValueOrZero(body, series) != v {
				settled = false
			}
		}
		if settled {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the reaper never sampled the operational series:\n%s", body)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func sampleValueOrZero(body, series string) float64 {
	for line := range strings.SplitSeq(body, "\n") {
		raw, ok := strings.CutPrefix(line, series+" ")
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0
		}
		return v
	}
	return 0
}
