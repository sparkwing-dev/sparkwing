package jobs

import (
	"context"
	"encoding/json"
	"fmt"

	"go.yaml.in/yaml/v3"
)

func (d *deployer) verifyClusterAdmission(ctx context.Context, rendered string) error {
	docs, err := splitRendered(rendered)
	if err != nil {
		return err
	}
	for _, doc := range docs {
		if doc.kind != "ValidatingAdmissionPolicy" && doc.kind != "ValidatingAdmissionPolicyBinding" {
			continue
		}
		live, err := d.kubectl(ctx, "get", doc.kind, doc.name, "-o", "json")
		if err != nil {
			return fmt.Errorf("read cluster admission %s/%s; apply the chart's cluster objects through kikd-infra before deploying: %w", doc.kind, doc.name, err)
		}
		want, err := clusterAdmissionSpec(doc.text)
		if err != nil {
			return err
		}
		got, err := clusterAdmissionSpec(live)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("cluster admission %s/%s differs from the chart; apply the chart's cluster objects through kikd-infra before deploying", doc.kind, doc.name)
		}
	}
	return nil
}

func clusterAdmissionSpec(raw string) (string, error) {
	var object struct{ Spec map[string]any }
	if err := yaml.Unmarshal([]byte(raw), &object); err != nil {
		return "", fmt.Errorf("decode cluster admission object: %w", err)
	}
	// safety: API defaults must not make an unchanged chart spec look different.
	for _, key := range []string{"matchConstraints", "matchResources"} {
		if match, ok := object.Spec[key].(map[string]any); ok {
			for _, selector := range []string{"namespaceSelector", "objectSelector"} {
				if match[selector] == nil {
					match[selector] = map[string]any{}
				}
			}
			if match["matchPolicy"] == nil {
				match["matchPolicy"] = "Equivalent"
			}
		}
	}
	body, err := json.Marshal(object.Spec)
	return string(body), err
}
