package charts

import (
	"slices"
	"strings"
	"testing"
)

func TestLauncherWarmCapacityRoleIsConfinedToWarmNamespace(t *testing.T) {
	for chart, prefix := range map[string]string{"./sparkwing-runner-bundle": "", "./sparkwing-full": "sparkwing-runner-bundle."} {
		settings := append(launcherImages(prefix), prefix+"launcher.serviceAccountName=cloud-launcher", prefix+"launcher.jobNamespace=customer-jobs", prefix+"launcher.warmNamespace=warm-only")
		found := 0
		for _, r := range renderedResources(t, helmRenderAll(t, chart, "sparkwing", "cloud", settings...)) {
			if r.Kind != "Role" || !strings.HasSuffix(r.Metadata.Name, "-warm-capacity") {
				continue
			}
			found++
			if r.Metadata.Namespace != "warm-only" || len(r.Rules) != 2 {
				t.Fatalf("%s warm capacity Role = %+v", chart, r)
			}
			owner := r.Rules[1]
			if !slices.Equal(owner.APIGroups, []string{"apps"}) || !slices.Equal(owner.Resources, []string{"replicasets"}) || !slices.Equal(owner.Verbs, []string{"get"}) {
				t.Fatalf("%s owner permissions = %+v", chart, owner)
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

func TestLauncherRefusesWarmNamespaceWithOperatorOrCustomerPods(t *testing.T) {
	for _, namespace := range []string{"default", "default-cloud-jobs"} {
		settings := append(launcherImages(""), "launcher.serviceAccountName=cloud-launcher", "launcher.warmNamespace="+namespace)
		if out := helmRenderError(t, "./sparkwing-runner-bundle", "sparkwing", settings...); !strings.Contains(out, "launcher.warmNamespace must differ") {
			t.Fatalf("render: %s", out)
		}
	}
}
