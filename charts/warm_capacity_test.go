package charts

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
	appsv1 "k8s.io/api/apps/v1"
	yaml "k8s.io/apimachinery/pkg/util/yaml"
)

func TestWarmCapacityRendersClassNodesAndSQLScaler(t *testing.T) {
	sets := []string{"warmCapacity.enabled=true", "warmCapacity.namespace=cloud-jobs", "warmCapacity.databaseSecret.name=warm-db", "controller.store=postgres", "controller.databaseSecret.name=controller-db"}
	out := helmRender(t, "./sparkwing-full", "templates/warm-capacity.yaml", "sparkwing", sets...)
	documents := strings.Split(out, "\n---")
	for _, class := range match.CloudSizes() {
		name := "sparkwing-sparkwing-full-warm-" + class.Label
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
		pod := deployment.Spec.Template.Spec
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
	for _, want := range []string{"kind: TriggerAuthentication", "parameter: connection", "name: \"warm-db\"", "type: postgresql", "metricType: AverageValue", "maxReplicaCount: 2147483647", "operator: DoesNotExist", "value: -1000", "sparkwing.dev/cloud-namespace", "sparkwing.dev/hardware-class", "sparkwing.dev/dedicated", "node-restriction.kubernetes.io/sparkwing-team-node"} {
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
