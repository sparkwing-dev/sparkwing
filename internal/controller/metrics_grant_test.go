package controller

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// An operator.metrics service credential is what a Prometheus scraper holds,
// so it must read /metrics and be refused by every other deployment route.
func TestOperatorMetricsCredentialScrapesButCannotOperate(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	raw := reviewSeed(t, e.Store)
	h := New(e.Store, nil).Handler()
	w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/operator/tokens", `{"name":"metrics-scraper","grants":["operator.metrics"]}`, http.StatusCreated)
	var issued credentialResponse
	if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}

	scrape := engineClaimHTTP(t, h, issued.Capability, "GET", "/metrics", "", http.StatusOK)
	if !strings.Contains(scrape.Body.String(), "sparkwing_pending_nodes") {
		t.Fatalf("metrics body lacks controller series: %.200s", scrape.Body.String())
	}

	param := regexp.MustCompile(`\{[^}]+\}`)
	refused := 0
	for _, route := range capability.Routes() {
		if route.Audience != capability.Controller || route.Owner != capability.OwnerDeployment || route.Path == "/metrics" {
			continue
		}
		path := param.ReplaceAllString(route.Path, "x")
		engineClaimHTTP(t, h, issued.Capability, route.Method, path, "{}", http.StatusForbidden)
		refused++
	}
	if refused == 0 {
		t.Fatal("no deployment routes were exercised")
	}
}
