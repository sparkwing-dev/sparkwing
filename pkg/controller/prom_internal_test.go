package controller

import (
	"context"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

func sampleValue(body, series string) (float64, bool) {
	for line := range strings.SplitSeq(body, "\n") {
		raw, ok := strings.CutPrefix(line, series+" ")
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

func mustSample(t *testing.T, body, series string) float64 {
	t.Helper()
	v, ok := sampleValue(body, series)
	if !ok {
		t.Fatalf("series %q absent from the exposition", series)
	}
	return v
}

func seriesCount(body, name string) int {
	n := 0
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, name+"{") || strings.HasPrefix(line, name+" ") {
			n++
		}
	}
	return n
}

// safety: every test here writes package-global metrics, so each one hands the
// registry back the way it found it rather than relying on test order.
func restoreGlobals(t *testing.T) {
	t.Helper()
	before := ledgerSnapshot.get()
	t.Cleanup(func() {
		liveRunners.sample(nil)
		setQueueDepth(nil)
		ledgerSnapshot.set(before)
	})
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

func TestRunnerLivenessDeletesDepartedSeries(t *testing.T) {
	restoreGlobals(t)

	liveRunners.sample([][]string{{"departing-runner"}})
	const series = `sparkwing_runners_live{label_set="departing-runner"}`
	if got := mustSample(t, scrapeRegistry(t), series); got != 1 {
		t.Fatalf("live gauge = %v, want 1 after the runner reported", got)
	}

	liveRunners.sample(nil)
	body := scrapeRegistry(t)
	if _, present := sampleValue(body, series); present {
		t.Errorf("a runner that stopped polling kept its series:\n%s", body)
	}
	if got := seriesCount(body, "sparkwing_runners_live"); got != 0 {
		t.Errorf("runners_live series = %d after every runner left, want 0", got)
	}
	if got := mustSample(t, body, "sparkwing_live_runners"); got != 0 {
		t.Errorf("fleet gauge = %v, want an empty fleet to read 0 rather than drop out", got)
	}
}

func TestLiveRunnersGaugeStaysPresentForAnEmptyFleet(t *testing.T) {
	restoreGlobals(t)

	liveRunners.sample([][]string{{"a"}, {"b"}, {"b"}})
	if got := mustSample(t, scrapeRegistry(t), "sparkwing_live_runners"); got != 3 {
		t.Errorf("fleet gauge = %v, want every runner across both label sets", got)
	}
	liveRunners.sample(nil)
	if _, present := sampleValue(scrapeRegistry(t), "sparkwing_live_runners"); !present {
		t.Error("the fleet gauge left the exposition, so an equality alert can never fire")
	}
}

func TestRunnerLivenessStaysBoundedAcrossSweeps(t *testing.T) {
	restoreGlobals(t)

	for sweep := range 20 {
		sets := make([][]string, 0, 8)
		for i := range 8 {
			sets = append(sets, []string{"sweep-" + strconv.Itoa(sweep) + "-runner-" + strconv.Itoa(i)})
		}
		liveRunners.sample(sets)
	}

	if got := seriesCount(scrapeRegistry(t), "sparkwing_runners_live"); got > maxRunnerLabelSets {
		t.Errorf("runners_live series = %d after 20 sweeps of fresh label sets, want at most %d",
			got, maxRunnerLabelSets)
	}
}

func TestRunnerLivenessEvictsTheSmallestSetsNotTheFirstAlphabetically(t *testing.T) {
	restoreGlobals(t)

	var sets [][]string
	for range 40 {
		sets = append(sets, []string{"fleet-prod"})
	}
	for i := range maxRunnerLabelSets * 2 {
		sets = append(sets, []string{"!hostile-" + strconv.Itoa(i)})
	}
	liveRunners.sample(sets)

	body := scrapeRegistry(t)
	if got := mustSample(t, body, `sparkwing_runners_live{label_set="fleet-prod"}`); got != 40 {
		t.Errorf("the busiest set reports %v, want 40: a set sorting first must not displace it", got)
	}
	if got := seriesCount(body, "sparkwing_runners_live"); got > maxRunnerLabelSets {
		t.Errorf("runners_live series = %d, want at most %d", got, maxRunnerLabelSets)
	}
	if got := mustSample(t, body, `sparkwing_runners_live{label_set="other"}`); got < 1 {
		t.Errorf("overflow series = %v, want the displaced sets summed onto it", got)
	}
}

func TestRunnerLivenessKeepsAnIncumbentOverATiedNewcomer(t *testing.T) {
	restoreGlobals(t)

	liveRunners.sample([][]string{{"quiet-incumbent"}})
	sets := [][]string{{"quiet-incumbent"}}
	for i := range maxRunnerLabelSets * 2 {
		sets = append(sets, []string{"!hostile-" + strconv.Itoa(i)})
	}
	liveRunners.sample(sets)

	body := scrapeRegistry(t)
	if got := mustSample(t, body, `sparkwing_runners_live{label_set="quiet-incumbent"}`); got != 1 {
		t.Errorf("the incumbent reports %v, want 1: a newcomer must not win the size tie", got)
	}
	if got := seriesCount(body, "sparkwing_runners_live"); got > maxRunnerLabelSets {
		t.Errorf("runners_live series = %d, want at most %d", got, maxRunnerLabelSets)
	}
}

func TestSetQueueDepthCoversEveryState(t *testing.T) {
	restoreGlobals(t)

	setQueueDepth(map[string]int{store.QueueStateReady: 3})
	body := scrapeRegistry(t)
	if got := mustSample(t, body, `sparkwing_queue_depth{state="ready"}`); got != 3 {
		t.Errorf("ready depth = %v, want 3", got)
	}
	for _, state := range store.QueueStates() {
		if state == store.QueueStateReady {
			continue
		}
		if got := mustSample(t, body, `sparkwing_queue_depth{state="`+state+`"}`); got != 0 {
			t.Errorf("%s depth = %v, want a state the sample omitted to read 0", state, got)
		}
	}
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

func TestClaimUnavailableCountsOnlyTheClaimRoutes503s(t *testing.T) {
	const series = "sparkwing_claim_unavailable_total"
	before := mustSample(t, scrapeRegistry(t), series)

	observeHTTPRequest(claimRoute, http.MethodPost, http.StatusNoContent, time.Millisecond)
	observeHTTPRequest("/api/v1/health", http.MethodGet, http.StatusServiceUnavailable, time.Millisecond)
	if got := mustSample(t, scrapeRegistry(t), series); got != before {
		t.Errorf("counter = %v, want %v: neither a healthy claim nor another route's 503 counts", got, before)
	}

	observeHTTPRequest(claimRoute, http.MethodPost, http.StatusServiceUnavailable, time.Millisecond)
	if got := mustSample(t, scrapeRegistry(t), series); got != before+1 {
		t.Errorf("counter = %v, want %v after one claim 503", got, before+1)
	}
}

func TestClaimWaitSkipsAReclaimAfterRequeue(t *testing.T) {
	const series = "sparkwing_node_claim_wait_seconds_count"
	claimable := time.Now().Add(-2 * time.Second)

	before := mustSample(t, scrapeRegistry(t), series)
	observeClaimWait(&store.Node{PlacementHoldFrom: &claimable, ClaimGeneration: 2})
	if got := mustSample(t, scrapeRegistry(t), series); got != before {
		t.Errorf("observations = %v, want %v: a re-claim reports the previous attempt's wait", got, before)
	}

	observeClaimWait(&store.Node{PlacementHoldFrom: &claimable, ClaimGeneration: 1})
	if got := mustSample(t, scrapeRegistry(t), series); got != before+1 {
		t.Errorf("observations = %v, want %v after a first claim", got, before+1)
	}
}

func TestClaimWaitPrefersTheHoldInstantOverReadyAt(t *testing.T) {
	const sum = "sparkwing_node_claim_wait_seconds_sum"
	held := time.Now().Add(-30 * time.Second)
	bumped := time.Now().Add(-time.Second)

	before := mustSample(t, scrapeRegistry(t), sum)
	observeClaimWait(&store.Node{PlacementHoldFrom: &held, ReadyAt: &bumped, ClaimGeneration: 1})
	if got := mustSample(t, scrapeRegistry(t), sum) - before; got < 20 {
		t.Errorf("observed wait = %vs, want the hold instant rather than the bumped ready_at", got)
	}
}

func TestNodeSecondsReportsLedgerSecondsAsCloud(t *testing.T) {
	restoreGlobals(t)

	beforeLocal := mustSample(t, scrapeRegistry(t), `sparkwing_node_seconds_total{placement="local"}`)
	ledgerSnapshot.set(store.CreditLedgerTotals{SettledSeconds: 4242})
	addLocalNodeSeconds(12.5)

	body := scrapeRegistry(t)
	if got := mustSample(t, body, `sparkwing_node_seconds_total{placement="cloud"}`); got != 4242 {
		t.Errorf("cloud seconds = %v, want the ledger's settled seconds 4242", got)
	}
	if got := mustSample(t, body, `sparkwing_node_seconds_total{placement="local"}`) - beforeLocal; got != 12.5 {
		t.Errorf("local seconds delta = %v, want 12.5", got)
	}
}

func TestCreditsCollectorReportsTheSampledLedger(t *testing.T) {
	restoreGlobals(t)

	ledgerSnapshot.set(store.CreditLedgerTotals{
		BalanceMicro:     7_000_000,
		GrantedFreeMicro: 2_000_000,
		GrantedPaidMicro: 9_000_000,
		ReversedMicro:    500_000,
		ReservedMicro:    1_200_000,
		ChargedMicro:     3_400_000,
		RefundedMicro:    600_000,
	})

	body := scrapeRegistry(t)
	for series, want := range map[string]float64{
		"sparkwing_credits_balance_micro":                    7_000_000,
		`sparkwing_credits_granted_micro_total{kind="free"}`: 2_000_000,
		`sparkwing_credits_granted_micro_total{kind="paid"}`: 9_000_000,
		"sparkwing_credits_reversed_micro_total":             500_000,
		"sparkwing_credits_reserved_micro_total":             1_200_000,
		"sparkwing_credits_charged_micro_total":              3_400_000,
		"sparkwing_credits_refunded_micro_total":             600_000,
	} {
		if got := mustSample(t, body, series); got != want {
			t.Errorf("%s = %v, want %v", series, got, want)
		}
	}
}

var documentedMetricRE = regexp.MustCompile("(?m)^\\| `(sparkwing_[a-z0-9_]+)` \\| \\w+ \\| ([^|]+)\\|")

// safety: the cache table lists metrics this registry does not serve, so the
// controller's own section is the only part of the page this compares against.
func documentedControllerMetrics(t *testing.T) map[string][]string {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "observability.md"))
	if err != nil {
		t.Fatalf("read observability.md: %v", err)
	}
	page := string(doc)
	start := strings.Index(page, "**Controller** (`sparkwing-controller`, Prometheus):")
	if start < 0 {
		t.Fatal("observability.md has no controller metrics section")
	}
	section := page[start:]
	if end := strings.Index(section, "**Cache** ("); end > 0 {
		section = section[:end]
	}

	out := map[string][]string{}
	for _, m := range documentedMetricRE.FindAllStringSubmatch(section, -1) {
		var labels []string
		for _, l := range strings.Split(strings.TrimSpace(m[2]), ",") {
			l = strings.Trim(strings.TrimSpace(l), "`")
			if l == "" || l == "(none)" {
				continue
			}
			labels = append(labels, l)
		}
		slices.Sort(labels)
		out[m[1]] = labels
	}
	return out
}

func TestDocumentedMetricsMatchTheRegistry(t *testing.T) {
	documented := documentedControllerMetrics(t)
	described := describedMetrics()

	if len(documented) < 20 {
		t.Fatalf("observability.md yielded %d controller metrics, so the table shape changed", len(documented))
	}
	for name, labels := range described {
		docLabels, ok := documented[name]
		if !ok {
			t.Errorf("the registry exports %q but observability.md does not document it", name)
			continue
		}
		if !slices.Equal(labels, docLabels) {
			t.Errorf("%s labels: registry %v, observability.md %v", name, labels, docLabels)
		}
	}
	for name := range documented {
		if _, ok := described[name]; !ok {
			t.Errorf("observability.md documents %q but the registry does not export it", name)
		}
	}
}

var (
	expositionTypeRE   = regexp.MustCompile(`^# TYPE ([a-zA-Z_:][a-zA-Z0-9_:]*) (counter|gauge|histogram|summary|untyped)$`)
	expositionSampleRE = regexp.MustCompile(
		`^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{.*\})? (-?(?:[0-9]+\.?[0-9]*(?:[eE][+-]?[0-9]+)?|Inf|NaN)|\+Inf)( [0-9]+)?$`)
)

// safety: reads the text format the way a scraper does, so a malformed line
// fails the test rather than passing a substring match.
func TestExpositionParsesAndNamesTheDocumentedSet(t *testing.T) {
	restoreGlobals(t)

	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	srv := New(st, nil)
	api := httptest.NewServer(srv.Handler())
	t.Cleanup(api.Close)

	if err := st.CreateRun(ctx, store.Run{
		ID: "run-expo", Pipeline: "expo", Status: "running", StartedAt: time.Now().Add(-time.Second),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-expo", NodeID: "node-a", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := st.MarkNodeReady(ctx, "run-expo", "node-a"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	if _, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{}, "holder-expo", time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}
	observeNodeClaim("expo")
	observeRunFinish("expo", "success", time.Second)
	liveRunners.sample([][]string{{"expo-runner"}})

	// safety: the request families mint no child until a request goes through
	// the logging middleware, so this test asks for one rather than leaning on
	// whatever another test in the package happened to send.
	health, err := http.Get(api.URL + "/api/v1/health")
	if err != nil {
		t.Fatalf("health request: %v", err)
	}
	_ = health.Body.Close()

	body := scrapeRegistry(t)
	families := map[string]string{}
	for i, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if m := expositionTypeRE.FindStringSubmatch(line); m != nil {
				families[m[1]] = m[2]
				continue
			}
			if !strings.HasPrefix(line, "# HELP ") && !strings.HasPrefix(line, "# EOF") {
				t.Errorf("/metrics line %d is neither a HELP nor a TYPE comment: %q", i+1, line)
			}
			continue
		}
		if !expositionSampleRE.MatchString(line) {
			t.Errorf("/metrics line %d is not a valid sample: %q", i+1, line)
		}
	}

	for name := range documentedControllerMetrics(t) {
		if _, ok := families[name]; !ok {
			t.Errorf("the exposition does not name %q, which observability.md documents", name)
		}
	}
}

func TestReaperAndCreditSamplerFillTheOperationalSeries(t *testing.T) {
	restoreGlobals(t)

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
	api := httptest.NewServer(srv.Handler())
	t.Cleanup(api.Close)
	claimWithLabels(t, api.URL, "pool-a", []string{"zone=b"})

	srv.sampleCreditLedger(ctx)
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
			if got, ok := sampleValue(body, series); !ok || got != v {
				settled = false
			}
		}
		if settled {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the samplers never reported %v:\n%s", slices.Sorted(maps.Keys(want)), body)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func claimWithLabels(t *testing.T, base, holder string, labels []string) {
	t.Helper()
	body := `{"holder_id":"` + holder + `","lease_secs":60,"labels":["` + strings.Join(labels, `","`) + `"]}`
	resp, err := http.Post(base+claimRoute, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("claim poll: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		t.Fatalf("claim poll status = %d", resp.StatusCode)
	}
}

// A node's charge window is what holds it in the reservation index and what
// keeps its reserved seconds out of the billing line, so a finish must release
// it even when the credential posting the finish is not the metered one that
// claimed the node.
func TestSettleFinishedNodeClearsTheWindowForAnUnmeteredFinisher(t *testing.T) {
	restoreGlobals(t)

	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if _, err := st.GrantCredits(ctx, store.CreditGrantPaid, 20_000_000, "invoice", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	_, tok, err := st.CreateTokenWith(ctx, "pool", store.TokenKindRunner,
		[]string{ScopeNodesClaim}, 0, time.Now(), store.TokenOptions{Metered: true})
	if err != nil {
		t.Fatalf("mint a metered token: %v", err)
	}
	claimant := store.ClaimIdentity{Principal: "pool", TokenPrefix: tok.Prefix}

	if err := st.CreateRun(ctx, store.Run{
		ID: "run-window", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-window", NodeID: "node-a", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := st.MarkNodeReady(ctx, "run-window", "node-a"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	if _, err := st.ClaimNextReadyNode(ctx, claimant, "holder-window", time.Minute, nil); err != nil {
		t.Fatalf("metered claim: %v", err)
	}
	before, err := st.NodeSettlement(ctx, "run-window", "node-a")
	if err != nil {
		t.Fatalf("NodeSettlement: %v", err)
	}
	if !before.ChargeWindowOpen {
		t.Fatal("the metered claim opened no charge window, so this test proves nothing")
	}

	if err := st.StartNode(ctx, "run-window", "node-a"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := st.FinishNode(ctx, "run-window", "node-a", "success", "", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}

	// safety: the request carries no credential at all, which is the shape a
	// revoked or un-metered finisher presents to this path.
	srv := New(st, nil)
	srv.settleFinishedNode(httptest.NewRequest(http.MethodPost, "/finish", nil), "run-window", "node-a")

	after, err := st.NodeSettlement(ctx, "run-window", "node-a")
	if err != nil {
		t.Fatalf("NodeSettlement after the finish: %v", err)
	}
	if after.ChargeWindowOpen {
		t.Error("the finish left the charge window open, so the node holds a reservation forever")
	}
}
