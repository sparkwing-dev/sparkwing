package runners_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/runners"
)

func TestValidate_AcceptsEveryRunnerType(t *testing.T) {
	f := runners.File{Runners: map[string]runners.Runner{
		"local": {Type: "local", Labels: []string{"local", "os=darwin"}},
		"cloud": {
			Type:    "kubernetes",
			Profile: "shared",
			Labels:  []string{"cloud", "os=linux"},
			Spec: runners.Spec{
				NodeSelector: map[string]string{"karpenter.sh/nodepool": "general"},
				Resources:    runners.Resources{Requests: map[string]string{"cpu": "2"}},
			},
		},
		"self": {Type: "static", Labels: []string{"self"}},
	}}
	if err := f.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidate_KubernetesRequiresProfile(t *testing.T) {
	f := runners.File{Runners: map[string]runners.Runner{
		"cloud": {Type: "kubernetes"},
	}}
	err := f.Validate()
	if err == nil || !strings.Contains(err.Error(), "profile") {
		t.Fatalf("Validate: want profile-required error, got %v", err)
	}
}

func TestValidate_RejectsUnknownType(t *testing.T) {
	f := runners.File{Runners: map[string]runners.Runner{
		"weird": {Type: "vm"},
	}}
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("Validate: want unknown-type error, got %v", err)
	}
}

func TestValidate_RejectsMissingType(t *testing.T) {
	f := runners.File{Runners: map[string]runners.Runner{
		"nope": {},
	}}
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "type is required") {
		t.Fatalf("Validate: want type-required error, got %v", err)
	}
}

func TestValidate_SpecOnlyOnKubernetes(t *testing.T) {
	f := runners.File{Runners: map[string]runners.Runner{
		"local": {
			Type: "local",
			Spec: runners.Spec{NodeSelector: map[string]string{"x": "y"}},
		},
	}}
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "spec block is only valid") {
		t.Fatalf("Validate: want spec-only-on-kubernetes error, got %v", err)
	}
}
