package charts

import (
	"reflect"
	"slices"
	"testing"
)

func TestCloudLauncherSecretPermissions(t *testing.T) {
	resources := renderedResources(t, helmRenderAll(t, "./sparkwing-runner-bundle", "sparkwing", "cloud", "launcher.serviceAccountName=cloud-launcher"))
	secretRule, customer := false, false
	for _, r := range resources {
		if r.Kind == "ServiceAccount" && r.Metadata.Name == "sparkwing-customer-job" {
			customer = true
			if len(r.Metadata.Annotations) != 0 {
				t.Fatal("customer account has cloud annotations")
			}
			if r.AutomountServiceAccountToken == nil || *r.AutomountServiceAccountToken {
				t.Fatal("customer account automounts Kubernetes credentials")
			}
		}
		for _, rule := range r.Rules {
			for _, resource := range rule.Resources {
				if resource == "secrets" {
					secretRule = true
					if !reflect.DeepEqual(rule.Verbs, []string{"create", "delete"}) {
						t.Fatalf("bootstrap Secret permissions = %v", rule.Verbs)
					}
				}
			}
		}
	}
	if !customer || !secretRule {
		t.Fatal("launcher or customer identity missing")
	}
}

func TestCloudLauncherProvisioningPermissions(t *testing.T) {
	resources := renderedResources(t, helmRenderAll(t, "./sparkwing-runner-bundle", "sparkwing", "cloud", "launcher.serviceAccountName=cloud-launcher"))
	wants := map[string][]string{"nodeclaims": {"get", "delete"}, "nodepools": {"get"}, "priorityclasses": {"list"}}
	for _, r := range resources {
		for _, rule := range r.Rules {
			for _, resource := range rule.Resources {
				if resource == "limitranges" {
					t.Fatal("unused LimitRange access remains")
				}
				if verbs, ok := wants[resource]; ok {
					if r.Kind != "ClusterRole" || !slices.Equal(verbs, rule.Verbs) {
						t.Fatalf("wrong permissions on %s: %+v", resource, rule)
					}
					delete(wants, resource)
				}
			}
		}
	}
	if len(wants) != 0 {
		t.Fatalf("missing provisioning permissions: %v", wants)
	}
}
