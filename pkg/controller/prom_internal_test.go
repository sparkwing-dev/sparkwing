package controller

import (
	"context"
	"encoding/hex"
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
	"testing/quick"
	"testing/synctest"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func meteringTestLicense(t *testing.T) *license.License {
	t.Helper()
	pub, priv := licensetest.NewKey(t)
	now := time.Now()
	raw := licensetest.Sign(t, priv, licensetest.Terms{
		Features: []string{license.FeatureMetering}, IssuedTo: "test",
		IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(24 * time.Hour),
	})
	return license.Resolve(raw, pub, now, nil)
}

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

func TestMetricsExcludeCredentialHashingBudget(t *testing.T) {
	if body := scrapeRegistry(t); strings.Contains(body, "sparkwing_auth_hashing_rejected_total") {
		t.Fatal("controller publishes obsolete credential hashing telemetry")
	}
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
	for _, kind := range []string{"account", "machine", "service", "operator", "local", "anonymous"} {
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

func TestSamplersFillTheOperationalSeries(t *testing.T) {
	restoreGlobals(t)

	ctx := context.Background()
	engine, billingTenant := engineRuntimeStore(t)
	st := engine.Store
	engineSQL(t, st, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run','node-a','pending',?,?)`, []byte(`{"id":"node-a"}`), []byte(`{}`))
	if _, err := billingTenant.RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 4_000_000, Reference: "trial", CreatedBy: "admin"}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	srv := New(st, nil).WithLicense(meteringTestLicense(t))
	srv.WithRunnerInventory(func(_ context.Context, team store.Team) (RunnerInventoryReader, error) {
		snapshot := capability.RunnerSnapshot{Now: time.Now()}
		if team == "a" {
			snapshot.Runners = []capability.RunnerObservation{{Team: "a", Runner: nodeproto.RunnerIdentity{Name: "pool-a", Type: "runner", Labels: []string{"zone=b"}}, LastSeen: snapshot.Now}}
		}
		return &runnerInventoryFake{team: team, snapshot: snapshot}, nil
	})

	srv.sampleCreditLedger(ctx)
	srv.sampleOperationalSeries(ctx)

	want := map[string]float64{
		`sparkwing_queue_depth{state="waiting"}`:             1,
		`sparkwing_credits_granted_micro_total{kind="free"}`: 4_000_000,
		`sparkwing_runners_live{label_set="zone=b"}`:         1,
	}
	body := scrapeRegistry(t)
	for _, series := range slices.Sorted(maps.Keys(want)) {
		got, ok := sampleValue(body, series)
		if !ok || got != want[series] {
			t.Errorf("%s = %v (reported %v), want %v:\n%s", series, got, ok, want[series], body)
		}
	}
}

func TestReaperSamplesTheOperationalSeries(t *testing.T) {
	restoreGlobals(t)

	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	setup := context.Background()
	if err := teststore.SeedRun(st, setup, store.DefaultTeam, store.Run{
		ID: "run-reaper-sweep", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := teststore.SeedNode(st, setup, store.Node{
		RunID: "run-reaper-sweep", NodeID: "node-a", Status: "pending",
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	srv := New(st, nil)
	const interval = time.Second
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			srv.runReaper(ctx, interval)
		}()

		// safety: the bubble's clock only moves once every goroutine is
		// parked, so this deadline carries the reaper past exactly one tick
		// and the Wait that follows returns when that sweep has finished.
		swept, done := context.WithTimeout(ctx, 2*interval)
		defer done()
		<-swept.Done()
		synctest.Wait()

		cancel()
		<-stopped
	})

	body := scrapeRegistry(t)
	const series = `sparkwing_queue_depth{state="waiting"}`
	if got, ok := sampleValue(body, series); !ok || got != 1 {
		t.Fatalf("%s = %v (reported %v) after a reaper sweep, want 1:\n%s", series, got, ok, body)
	}
}

const claimRoute = "/api/v1/runners/claim"

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

// safety: read from the collectors the registry serves rather than from the
// exposition, so a metric that has minted no child yet is still checked.
func describedMetrics() map[string][]string {
	descs := make(chan *prometheus.Desc, 64)
	out := map[string][]string{}
	for _, c := range sparkwingCollectors {
		go func() {
			c.Describe(descs)
			close(descs)
		}()
		for d := range descs {
			m := describedNameRE.FindStringSubmatch(d.String())
			if m == nil {
				continue
			}
			var labels []string
			for _, l := range strings.Split(m[2], ",") {
				if l = strings.TrimSpace(l); l != "" {
					labels = append(labels, l)
				}
			}
			slices.Sort(labels)
			out[m[1]] = labels
		}
		descs = make(chan *prometheus.Desc, 64)
	}
	return out
}

var describedNameRE = regexp.MustCompile(`fqName: "([^"]+)".*variableLabels: \{([^}]*)\}`)

func TestRequestPrincipalMetricsCountsAuthenticatedPolicy(t *testing.T) {
	s, raw := credentialSessionServer(t)
	h := s.Handler()
	const series = `sparkwing_requests_by_principal_total{credential="account"}`
	before := mustSample(t, scrapeRegistry(t), series)
	for _, tc := range []struct {
		method, path, raw string
		status            int
	}{
		{"GET", "/api/v1/auth/whoami", raw, 200},
		{"POST", "/api/v1/runners/claim", raw, 403},
		{"GET", "/api/v1/auth/whoami", "unknown", 401},
		{"GET", "/api/v1/no-such-route", raw, 404},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Session "+tc.raw)
		r.Header.Set("Content-Type", "application/json")
		if tc.method == "POST" {
			r.Header.Set("Origin", "http://example.com")
			r.Header.Set(csrfHeaderName, "session-csrf")
			r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "session-csrf"})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %s = %d %s", tc.method, tc.path, w.Code, w.Body)
		}
	}
	if after := mustSample(t, scrapeRegistry(t), series); after != before+2 {
		t.Fatalf("authenticated requests = %v, want %v", after, before+2)
	}
	if strings.Contains(scrapeRegistry(t), "sparkwing_auth_token_cache_total") {
		t.Fatal("retired token cache metric remains")
	}
}

func TestRequestPrincipalMetricLabelsProperty(t *testing.T) {
	allowed := []string{"account", "machine", "service", "operator", "local", "anonymous", "other"}
	property := func(input []byte, known bool, choice uint8) bool {
		private := "private-" + hex.EncodeToString(input)
		kind := private
		if known {
			kind = allowed[int(choice)%len(allowed)]
		}
		notePolicyPrincipal(context.Background(), capability.Principal{Capability: capability.Capability{Prefix: private, Team: private, Subject: capability.Subject{Kind: capability.SubjectKind(kind), ID: private}}})
		families, err := metricsRegistry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, family := range families {
			if family.GetName() != "sparkwing_requests_by_principal_total" {
				continue
			}
			if len(family.Metric) != len(allowed) {
				return false
			}
			for _, metric := range family.Metric {
				if len(metric.Label) != 1 || metric.Label[0].GetName() != "credential" || !slices.Contains(allowed, metric.Label[0].GetValue()) {
					return false
				}
			}
			return true
		}
		return false
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}
