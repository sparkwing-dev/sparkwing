package charts

import (
	"reflect"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	"sigs.k8s.io/yaml"

	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
)

func TestChartInstallsVerifiedCloudAdmissionGuard(t *testing.T) {
	for chart, settings := range map[string][]string{
		"./sparkwing-runner-bundle": {"launcher.serviceAccountName=cloud-launcher"},
		"./sparkwing-full":          {"sparkwing-runner-bundle.launcher.serviceAccountName=cloud-launcher"},
	} {
		t.Run(chart, func(t *testing.T) {
			for _, namespace := range []string{"cloud-jobs", "other-cloud-jobs"} {
				rendered := helmRenderAll(t, chart, "sparkwing", namespace, settings...)
				policyFound, bindingFound := false, false
				for _, document := range strings.Split(rendered, "\n---") {
					var metadata struct{ Kind, APIVersion string }
					if err := yaml.Unmarshal([]byte(document), &metadata); err != nil {
						t.Fatal(err)
					}
					switch metadata.Kind {
					case "ValidatingAdmissionPolicy":
						policyFound = true
						var policy admissionv1.ValidatingAdmissionPolicy
						if err := yaml.Unmarshal([]byte(document), &policy); err != nil {
							t.Fatal(err)
						}
						want := launcher.AdmissionExemptionPolicy(namespace)
						if metadata.APIVersion != "admissionregistration.k8s.io/v1" || policy.Name != want.Name || !reflect.DeepEqual(policy.Spec, want.Spec) {
							t.Errorf("rendered policy differs from the launcher's required guard for %s", namespace)
						}
					case "ValidatingAdmissionPolicyBinding":
						bindingFound = true
						var binding admissionv1.ValidatingAdmissionPolicyBinding
						if err := yaml.Unmarshal([]byte(document), &binding); err != nil {
							t.Fatal(err)
						}
						want := launcher.AdmissionExemptionBinding(namespace)
						if metadata.APIVersion != "admissionregistration.k8s.io/v1" || binding.Name != want.Name || !reflect.DeepEqual(binding.Spec, want.Spec) {
							t.Errorf("rendered binding differs from the launcher's required guard for %s", namespace)
						}
					}
				}
				if !policyFound || !bindingFound {
					t.Errorf("launcher guard missing: policy=%v binding=%v", policyFound, bindingFound)
				}
			}
		})
	}
}

func TestChartOmitsCloudAdmissionGuardWithoutLauncher(t *testing.T) {
	for _, chart := range []string{"./sparkwing-runner-bundle", "./sparkwing-full"} {
		for _, resource := range renderedResources(t, helmRenderAll(t, chart, "sparkwing", "cloud-jobs")) {
			if resource.Kind == "ValidatingAdmissionPolicy" || resource.Kind == "ValidatingAdmissionPolicyBinding" {
				t.Errorf("%s installed Cloud admission guard without a launcher", chart)
			}
		}
	}
}
