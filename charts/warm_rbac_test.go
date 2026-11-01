package charts

import (
	"slices"
	"strings"
	"testing"
)

func TestLauncherWarmCapacityRoleIsConfinedToOperatorNamespace(t *testing.T) {
	for chart, prefix := range map[string]string{"./sparkwing-runner-bundle": "", "./sparkwing-full": "sparkwing-runner-bundle."} {
		settings := append(launcherImages(prefix), prefix+"launcher.serviceAccountName=cloud-launcher", prefix+"launcher.jobNamespace=customer-jobs")
		found := 0
		for _, r := range renderedResources(t, helmRenderAll(t, chart, "sparkwing", "cloud", settings...)) {
			if r.Kind != "Role" || !strings.HasSuffix(r.Metadata.Name, "-warm-capacity") {
				continue
			}
			found++
			if r.Metadata.Namespace != "cloud" || len(r.Rules) != 1 {
				t.Fatalf("%s warm capacity Role = %+v", chart, r)
			}
			rule := r.Rules[0]
			if !slices.Equal(rule.APIGroups, []string{""}) || !slices.Equal(rule.Resources, []string{"pods"}) || !slices.Equal(rule.Verbs, []string{"get", "delete"}) {
				t.Fatalf("%s warm capacity permissions = %+v", chart, rule)
			}
		}
		if found != 1 {
			t.Fatalf("%s warm capacity Roles = %d, want 1", chart, found)
		}
	}
}
