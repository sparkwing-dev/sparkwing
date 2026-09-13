package controller_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var documentedMetricRE = regexp.MustCompile("(?m)^\\| `(sparkwing_[a-z0-9_]+)` \\|")

func documentedControllerMetrics(t *testing.T) []string {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "observability.md"))
	if err != nil {
		t.Fatalf("read observability.md: %v", err)
	}
	var names []string
	for _, m := range documentedMetricRE.FindAllStringSubmatch(string(doc), -1) {
		names = append(names, m[1])
	}
	slices.Sort(names)
	return slices.Compact(names)
}

var (
	expositionTypeRE   = regexp.MustCompile(`^# TYPE ([a-zA-Z_:][a-zA-Z0-9_:]*) (counter|gauge|histogram|summary|untyped)$`)
	expositionSampleRE = regexp.MustCompile(
		`^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{.*\})? (-?(?:[0-9]+\.?[0-9]*(?:[eE][+-]?[0-9]+)?|Inf|NaN)|\+Inf)( [0-9]+)?$`)
)

// safety: reads the text format the way a scraper does, so a malformed line
// fails the test rather than passing a substring match.
func parseExposition(t *testing.T, body string) map[string]string {
	t.Helper()
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
	return families
}

func TestMetrics_ExpositionCoversTheDocumentedSet(t *testing.T) {
	base, st, cleanup := newTestServer(t)
	defer cleanup()
	ctx := context.Background()

	mustPostJSON(t, base+"/api/v1/runs", store.Run{
		ID: "run-doc-metrics", Pipeline: "doc-metrics",
		Status: "running", StartedAt: time.Now().Add(-time.Second),
	}, http.StatusCreated)
	mustPostJSON(t, base+"/api/v1/runs/run-doc-metrics/finish",
		map[string]any{"status": "success"}, http.StatusNoContent)

	seedRunNode(t, st, "run-doc-claim", "node-a")
	c := client.New(base, nil)
	if err := c.MarkNodeReady(ctx, "run-doc-claim", "node-a"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}
	claimed, err := c.ClaimNode(ctx, "pod-doc", nil, 30*time.Second, nil)
	if err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}
	if claimed == nil {
		t.Fatal("ClaimNode returned no node, so the claim series stay empty")
	}

	families := parseExposition(t, scrape(t, base))

	documented := documentedControllerMetrics(t)
	if len(documented) < 10 {
		t.Fatalf("observability.md yielded %d controller metrics, so the table shape changed", len(documented))
	}
	for _, name := range documented {
		if _, ok := families[name]; !ok {
			t.Errorf("observability.md documents %q but /metrics does not export it", name)
		}
	}
	for name := range families {
		if !strings.HasPrefix(name, "sparkwing_") {
			continue
		}
		if !slices.Contains(documented, name) {
			t.Errorf("/metrics exports %q but observability.md does not document it", name)
		}
	}
}

func TestMetrics_OperationalSeriesCarryTheirLabelSets(t *testing.T) {
	base, st, cleanup := newTestServer(t)
	defer cleanup()
	ctx := context.Background()

	seedRunNode(t, st, "run-ops", "node-a")
	c := client.New(base, nil)
	if err := c.MarkNodeReady(ctx, "run-ops", "node-a"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}
	if _, err := c.ClaimNode(ctx, "pod-ops", []string{"gpu", "zone=b"}, 30*time.Second, nil); err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}

	body := scrape(t, base)
	for _, want := range []string{
		`sparkwing_queue_depth{state="waiting"}`,
		`sparkwing_queue_depth{state="ready"}`,
		`sparkwing_queue_depth{state="claimed"}`,
		`sparkwing_queue_depth{state="running"}`,
		`sparkwing_queue_depth{state="approval_pending"}`,
		`sparkwing_node_claim_wait_seconds_count{placement="local"}`,
		`sparkwing_node_claim_wait_seconds_count{placement="cloud"}`,
		`sparkwing_node_seconds_total{placement="local"}`,
		`sparkwing_node_seconds_total{placement="cloud"}`,
		`sparkwing_credits_reserved_micro_total{principal_kind="free"}`,
		`sparkwing_credits_reserved_micro_total{principal_kind="paid"}`,
		`sparkwing_credits_charged_micro_total{principal_kind="paid"}`,
		`sparkwing_credits_granted_micro_total{kind="free"}`,
		`sparkwing_credits_granted_micro_total{kind="paid"}`,
		`sparkwing_runners_live{label_set=""}`,
		`sparkwing_requests_by_principal_total{credential="runner"}`,
		"sparkwing_claim_unavailable_total",
		"sparkwing_credits_balance_micro",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}

	if got := metricSampleValue(t, body, `sparkwing_node_claim_wait_seconds_count{placement="local"}`); got < 1 {
		t.Errorf("claim wait observations = %v, want the claim just served to be counted", got)
	}
}

func TestMetrics_ClaimUnavailableCountsOnlyTheClaimRoute(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	before := metricSampleValue(t, scrape(t, base), "sparkwing_claim_unavailable_total")
	resp := mustGet(t, base+"/api/v1/health")
	resp.Body.Close()
	if got := metricSampleValue(t, scrape(t, base), "sparkwing_claim_unavailable_total"); got != before {
		t.Errorf("claim 503 counter = %v after a healthy request, want %v", got, before)
	}
}
