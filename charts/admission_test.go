package charts

import "testing"

func TestCloudChartsNeedNoAdmissionPolicies(t *testing.T) {
	for chart, settings := range map[string][]string{
		"./sparkwing-runner-bundle": {"launcher.serviceAccountName=cloud-launcher"},
		"./sparkwing-full":          {"sparkwing-runner-bundle.launcher.serviceAccountName=cloud-launcher"},
	} {
		for _, r := range renderedResources(t, helmRenderAll(t, chart, "sparkwing", "cloud", settings...)) {
			switch r.Kind {
			case "ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding", "MutatingAdmissionPolicy", "MutatingAdmissionPolicyBinding":
				t.Errorf("%s renders %s", chart, r.Kind)
			}
			for _, rule := range r.Rules {
				for _, group := range rule.APIGroups {
					if group == "admissionregistration.k8s.io" {
						t.Errorf("%s grants admission permissions", chart)
					}
				}
			}
		}
	}
}
