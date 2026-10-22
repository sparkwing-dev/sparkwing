package nodeproto

import "testing"

func TestJobArgumentNamespaceAllowsJobRenaming(t *testing.T) {
	known := ArgumentDescription{Name: "image", Type: "string", Required: true, JobID: "discovery"}
	for _, test := range []struct {
		name    string
		jobs    []JobSpec
		invalid bool
	}{
		{"renamed", []JobSpec{{ID: "deploy-prod", Arguments: []ArgumentDescription{{Name: "image", Type: "string", Required: true, JobID: "deploy-prod"}}}}, false},
		{"duplicate", []JobSpec{
			{ID: "deploy-prod", Arguments: []ArgumentDescription{{Name: "image", Type: "string", Required: true, JobID: "deploy-prod"}}},
			{ID: "deploy-stage", Arguments: []ArgumentDescription{{Name: "image", Type: "string", Required: true, JobID: "deploy-stage"}}},
		}, true},
		{"changed type", []JobSpec{{ID: "deploy-prod", Arguments: []ArgumentDescription{{Name: "image", Type: "int", Required: true, JobID: "deploy-prod"}}}}, true},
		{"changed secrecy", []JobSpec{{ID: "deploy-prod", Arguments: []ArgumentDescription{{Name: "image", Type: "string", Required: true, Secret: true, JobID: "deploy-prod"}}}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateJobArgumentNamespace([]ArgumentDescription{known}, test.jobs)
			if (err != nil) != test.invalid {
				t.Fatalf("namespace error = %v, invalid = %v", err, test.invalid)
			}
		})
	}
}
