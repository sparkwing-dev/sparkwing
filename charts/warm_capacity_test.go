package charts

import (
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	yaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestWarmCapacityRendersClassNodesAndSQLScaler(t *testing.T) {
	sets := []string{"warmCapacity.enabled=true", "warmCapacity.namespace=cloud-jobs", "warmCapacity.databaseSecret.name=warm-db", "controller.store=postgres", "controller.databaseSecret.name=controller-db"}
	out := helmRender(t, "./sparkwing-full", "templates/warm-capacity.yaml", "sparkwing", sets...)
	documents := strings.Split(out, "\n---")
	for _, class := range match.CloudSizes() {
		name := fmt.Sprintf("sparkwing-cloud-warm-%d", class.CreditClass)
		var deployment appsv1.Deployment
		found := false
		for _, doc := range documents {
			var candidate appsv1.Deployment
			if err := yaml.Unmarshal([]byte(doc), &candidate); err != nil {
				t.Fatal(err)
			}
			if candidate.Kind == "Deployment" && candidate.Name == name {
				deployment = candidate
				found = true
			}
		}
		if !found {
			t.Fatalf("missing Deployment %s", name)
		}
		if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 {
			t.Fatal("warm nodes must start Off")
		}
		if deployment.Namespace != "default-cloud-warm" {
			t.Fatalf("warm namespace = %q", deployment.Namespace)
		}
		pod := deployment.Spec.Template.Spec
		if len(pod.Volumes) != 0 || pod.PriorityClassName != "sparkwing-cloud-warm" {
			t.Fatal("untrusted warm placeholder shape")
		}
		if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken || pod.PriorityClassName == "" {
			t.Fatal("placeholder identity or priority")
		}
		resources := pod.Containers[0].Resources.Requests
		if resources.Cpu().Value() != class.CreditClass || resources.Memory().Value() != class.Resources.MemoryBytes {
			t.Fatalf("%s resources %v", class.Label, resources)
		}
		if !strings.Contains(out, fmt.Sprintf("SELECT desired_replicas FROM warm_pool_desired WHERE hardware_class = '%s'", class.Label)) {
			t.Fatalf("missing class SQL %s", class.Label)
		}
	}
	for _, want := range []string{"kind: TriggerAuthentication", "parameter: connection", "name: \"warm-db\"", "type: postgresql", "metricType: AverageValue", "maxReplicaCount: 2147483647", "operator: DoesNotExist", "value: -1000", "sparkwing.dev/launcher-namespace", "node-restriction.kubernetes.io/sparkwing-cloud-class", "sparkwing.dev/dedicated", "node-restriction.kubernetes.io/sparkwing-team-node"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestWarmCapacityDefaultCreatesNoResources(t *testing.T) {
	out := helmRenderAll(t, "./sparkwing-full", "sparkwing", "sparkwing")
	if strings.Contains(out, "kind: ScaledObject") || strings.Contains(out, "warm-placeholder") {
		t.Fatal("warm capacity enabled by default")
	}
}

func TestWarmCapacitySharesPriorityClassAcrossReleases(t *testing.T) {
	out := helmRender(t, "./sparkwing-full", "templates/warm-capacity.yaml", "staging",
		"fullnameOverride=custom", "sparkwing-runner-bundle.controller.url=http://custom-controller", "warmCapacity.enabled=true", "warmCapacity.createPriorityClass=false",
		"sparkwing-runner-bundle.enabled=false", "sparkwing-runner-bundle.launcher.enabled=true",
		"sparkwing-runner-bundle.launcher.warmNamespace=staging-warm",
		"warmCapacity.namespace=staging-jobs", "warmCapacity.databaseSecret.name=warm-reader",
		"controller.store=postgres", "controller.databaseSecret.name=controller-db")
	if strings.Contains(out, "kind: PriorityClass") {
		t.Fatal("secondary release creates shared PriorityClass")
	}
	for _, want := range []string{"kind: Namespace", "name: sparkwing-cloud-warm-2", "namespace: staging-warm", "priorityClassName: sparkwing-cloud-warm"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s", want)
		}
	}
}
