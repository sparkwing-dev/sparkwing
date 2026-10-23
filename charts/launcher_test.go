package charts

import (
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/pkg/limits"
)

func TestCloudLauncherSecretPermissions(t *testing.T) {
	resources := renderedResources(t, helmRenderAll(t, "./sparkwing-runner-bundle", "sparkwing", "cloud", append(launcherImages(""), "launcher.serviceAccountName=cloud-launcher")...))
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

// The Job controller cannot create a Job Pod until its service account exists
// in the job namespace, so every install that renders the admission policies
// also renders that account there, without a token.
func TestCustomerJobServiceAccountInJobNamespace(t *testing.T) {
	for chart, prefix := range map[string]string{"./sparkwing-runner-bundle": "", "./sparkwing-full": "sparkwing-runner-bundle."} {
		for jobs, extra := range map[string][]string{"cloud-cloud-jobs": nil, "customer-jobs": {prefix + "launcher.jobNamespace=customer-jobs"}} {
			settings := append(append(launcherImages(prefix), prefix+"launcher.serviceAccountName=cloud-launcher"), extra...)
			found := 0
			for _, r := range renderedResources(t, helmRenderAll(t, chart, "sparkwing", "cloud", settings...)) {
				if r.Kind != "ServiceAccount" || r.Metadata.Name != launcher.ServiceAccount {
					continue
				}
				found++
				if r.Metadata.Namespace != jobs || r.AutomountServiceAccountToken == nil || *r.AutomountServiceAccountToken {
					t.Errorf("%s: %s must live in %s without a token: namespace=%q automount=%v", chart, launcher.ServiceAccount, jobs, r.Metadata.Namespace, r.AutomountServiceAccountToken)
				}
			}
			if found != 1 {
				t.Errorf("%s renders %d %s accounts for job namespace %s, want 1", chart, found, launcher.ServiceAccount, jobs)
			}
		}
	}
}

func TestCloudLauncherProvisioningPermissions(t *testing.T) {
	resources := renderedResources(t, helmRenderAll(t, "./sparkwing-runner-bundle", "sparkwing", "cloud", append(launcherImages(""), "launcher.serviceAccountName=cloud-launcher")...))
	wants := map[string][]string{"nodeclaims": {"get", "delete"}, "nodepools": {"get"}, "nodes": {"get", "list", "update", "patch"}}
	for _, r := range resources {
		for _, rule := range r.Rules {
			for _, resource := range rule.Resources {
				if resource == "limitranges" || resource == "priorityclasses" || resource == "namespaces" {
					t.Fatalf("unused %s access remains", resource)
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

func TestCloudLauncherDefaultPollHonorsTheControllerIdleInterval(t *testing.T) {
	data, err := os.ReadFile("sparkwing-runner-bundle/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		Launcher struct {
			PollInterval string `json:"pollInterval"`
		} `json:"launcher"`
	}
	if err := utilyaml.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	poll, err := time.ParseDuration(values.Launcher.PollInterval)
	if err != nil || poll < controller.DefaultMaxIdleClaimPoll {
		t.Fatalf("launcher pollInterval %q polls faster than the controller's idle interval %s: %v", values.Launcher.PollInterval, controller.DefaultMaxIdleClaimPoll, err)
	}
}

func TestTeamNodePoolExampleOutlivesTheAwardLease(t *testing.T) {
	data, err := os.ReadFile("sparkwing-full/examples/team-nodepool.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var pool struct {
		Spec struct {
			Disruption struct {
				ConsolidationPolicy string `json:"consolidationPolicy"`
				ConsolidateAfter    string `json:"consolidateAfter"`
			} `json:"disruption"`
		} `json:"spec"`
	}
	if err := utilyaml.Unmarshal(data, &pool); err != nil {
		t.Fatal(err)
	}
	after, err := time.ParseDuration(pool.Spec.Disruption.ConsolidateAfter)
	if err != nil || pool.Spec.Disruption.ConsolidationPolicy != "WhenEmpty" || after <= limits.DefaultLease {
		t.Fatalf("team NodePool disruption %+v must consolidate only empty nodes after more than %s: %v", pool.Spec.Disruption, limits.DefaultLease, err)
	}
}
