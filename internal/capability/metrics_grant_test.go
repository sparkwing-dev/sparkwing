package capability

import (
	"slices"
	"testing"
)

func TestOperatorMetricsGrantReadsOnlyMetrics(t *testing.T) {
	var accepted []string
	for _, route := range Routes() {
		for _, access := range route.Access {
			if slices.Contains(access.Grants, OperatorMetrics) {
				accepted = append(accepted, route.Method+" "+route.Path)
			}
		}
	}
	if !slices.Equal(accepted, []string{"GET /metrics"}) {
		t.Fatalf("operator.metrics is accepted by %v, want only GET /metrics", accepted)
	}
	route, ok := LookupRoute("GET", "/metrics")
	if !ok {
		t.Fatal("GET /metrics has no policy")
	}
	if !slices.ContainsFunc(route.Access, func(a AccessRule) bool {
		return a.Credential == CredentialDeployment && slices.Equal(a.Grants, []Grant{OperatorLimits})
	}) {
		t.Fatalf("GET /metrics no longer accepts operator.limits: %+v", route.Access)
	}
}
