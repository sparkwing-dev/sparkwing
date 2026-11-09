package nodeproto

import (
	"testing"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestJobArgumentNamespaceAllowsJobRenaming(t *testing.T) {
	known := nodewire.ArgumentDescription{Name: "image", Type: "string", Required: true, JobID: "discovery"}
	for _, test := range []struct {
		name    string
		jobs    []nodewire.JobSpec
		invalid bool
	}{
		{"renamed", []nodewire.JobSpec{{ID: "deploy-prod", Arguments: []nodewire.ArgumentDescription{{Name: "image", Type: "string", Required: true, JobID: "deploy-prod"}}}}, false},
		{"duplicate", []nodewire.JobSpec{
			{ID: "deploy-prod", Arguments: []nodewire.ArgumentDescription{{Name: "image", Type: "string", Required: true, JobID: "deploy-prod"}}},
			{ID: "deploy-stage", Arguments: []nodewire.ArgumentDescription{{Name: "image", Type: "string", Required: true, JobID: "deploy-stage"}}},
		}, true},
		{"changed type", []nodewire.JobSpec{{ID: "deploy-prod", Arguments: []nodewire.ArgumentDescription{{Name: "image", Type: "int", Required: true, JobID: "deploy-prod"}}}}, true},
		{"changed secrecy", []nodewire.JobSpec{{ID: "deploy-prod", Arguments: []nodewire.ArgumentDescription{{Name: "image", Type: "string", Required: true, Secret: true, JobID: "deploy-prod"}}}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateInitialJobArguments([]nodewire.ArgumentDescription{known}, test.jobs)
			if (err != nil) != test.invalid {
				t.Fatalf("namespace error = %v, invalid = %v", err, test.invalid)
			}
		})
	}
}

func TestInitialJobArgumentsRefuseUndiscoveredDeclarations(t *testing.T) {
	secret := "conditional-secret-default"
	jobs := []nodewire.JobSpec{{ID: "conditional", Arguments: []nodewire.ArgumentDescription{{Name: "token", Type: "string", Secret: true, Default: &secret, JobID: "conditional"}}}}
	if err := ValidateInitialJobArguments(nil, jobs); err == nil {
		t.Fatal("initial plan accepted an undiscovered secret default")
	}
}
