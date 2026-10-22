package runspec

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestDeclarationRefusalOnlyAppliesToSelectedDefaults(t *testing.T) {
	for _, value := range []string{"a", "ab***cd", ""} {
		t.Run(value, func(t *testing.T) {
			masker := secrets.NewMasker()
			gate, err := NewDeclarationGate(nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{
				{Name: "selected", Args: []nodeproto.ArgumentDescription{}},
				{Name: "other", Args: []nodeproto.ArgumentDescription{{Name: "token", Type: "string", Secret: true, Default: &value}}},
			}}, masker)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := gate.Resolve("selected", nil, nil); err != nil {
				t.Fatal(err)
			}
			_, err = gate.Resolve("other", nil, nil)
			if value != "a" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, secrets.ErrInvalidValue) {
				t.Fatalf("selected default not refused: %v", err)
			}
		})
	}
}

func TestDeclarationProjectionRetainsValidSiblingDefaults(t *testing.T) {
	value := "private-sibling"
	gate, err := NewDeclarationGate(nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{
		{Name: "selected", Args: []nodeproto.ArgumentDescription{}},
		{Name: "other", Args: []nodeproto.ArgumentDescription{{Name: "token", Type: "string", Secret: true, Default: &value}}},
	}}, secrets.NewMasker())
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := gate.Resolve("selected", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh := secrets.NewMasker()
	for _, value := range resolved.Registration.Values {
		if err := fresh.Register(value); err != nil {
			t.Fatal(err)
		}
	}
	if got := fresh.Mask(value); got != "***" {
		t.Fatalf("fresh host lost sibling declaration: %q", got)
	}
	if got := gate.Error(errors.New(value)).Error(); strings.Contains(got, value) {
		t.Fatal(got)
	}
}

func TestDeclarationPresentationHidesUnselectedSecretDefaults(t *testing.T) {
	for _, value := range []string{"abc", "prefix-***-suffix"} {
		t.Run(value, func(t *testing.T) {
			doc := nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{
				{Name: "selected", Args: []nodeproto.ArgumentDescription{}},
				{Name: "other", Args: []nodeproto.ArgumentDescription{{Name: "token", Type: "string", Secret: true, Default: &value}}},
			}}
			gate, err := NewDeclarationGate(doc, secrets.NewMasker())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := gate.Resolve("selected", nil, nil); err != nil {
				t.Fatalf("unselected default refused selected pipeline: %v", err)
			}
			presented := gate.Presentation().Pipelines[1].Args[0].Default
			if presented == nil {
				t.Fatal("secret default missing")
			}
			if *presented != "***" {
				t.Fatalf("secret default presented as %q", *presented)
			}
		})
	}
}

func TestUnselectedListDefaultRegistersEveryValidCandidate(t *testing.T) {
	value := "x, legitimate-private-token ,second-private-token"
	masker := secrets.NewMasker()
	gate, err := NewDeclarationGate(nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{
		{Name: "selected", Args: []nodeproto.ArgumentDescription{}},
		{Name: "other", Args: []nodeproto.ArgumentDescription{{Name: "tokens", Type: "[]string", Secret: true, Default: &value}}},
	}}, masker)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := gate.Resolve("selected", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	restored := secrets.NewMasker()
	for _, candidate := range resolved.Registration.Values {
		if err := restored.Register(candidate); err != nil {
			t.Fatal(err)
		}
	}
	for _, candidate := range []string{value, "legitimate-private-token", "second-private-token"} {
		for _, m := range []*secrets.Masker{masker, restored} {
			if got := m.Mask(candidate); got != "***" {
				t.Fatalf("candidate %q was not registered: %q", candidate, got)
			}
		}
	}
	if got := restored.Mask("x"); got != "x" {
		t.Fatalf("short sibling item was registered: %q", got)
	}
	if _, err := gate.Resolve("other", nil, nil); !errors.Is(err, secrets.ErrInvalidValue) {
		t.Fatalf("selected short list item was not refused: %v", err)
	}
}

func TestIncompleteDiscoveryWithholdsEveryPresentationField(t *testing.T) {
	private := "undiscovered-private-value"
	gate, err := NewDeclarationGate(nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{
		{Name: private, Args: []nodeproto.ArgumentDescription{{Name: private, Short: private, JobID: private, Type: private, Description: private, Default: &private, Enum: []string{private}}}, Short: private, Help: private, Examples: []nodeproto.UsageExample{{Command: private, Comment: private}}},
		{Name: "broken", Error: &apierr.Error{Code: apierr.SDKError, Message: private}},
	}}, secrets.NewMasker())
	if err != nil {
		t.Fatal(err)
	}
	public, err := json.Marshal(gate.Presentation())
	if err != nil || strings.Contains(string(public), private) || strings.Contains(string(public), "broken") {
		t.Fatalf("unclassified presentation escaped: %s %v", public, err)
	}
	if !strings.Contains(string(public), "details withheld") {
		t.Fatalf("incomplete discovery was not explained: %s", public)
	}
	if _, found := gate.Declarations().Pipeline(private); !found {
		t.Fatal("presentation changed private routing declarations")
	}
}

func TestEmptySecretArgumentIsNoValue(t *testing.T) {
	for _, kind := range []string{"string", "[]string"} {
		t.Run(kind, func(t *testing.T) {
			defaultValue := "private-default"
			resolved, err := RegisterArguments([]nodeproto.ArgumentDescription{{Name: "token", Type: kind, Secret: true, Default: &defaultValue}}, map[string]string{"token": ""}, nil, secrets.NewMasker())
			if err != nil {
				t.Fatal(err)
			}
			if resolved.Args["token"] != "" || !slices.Equal(resolved.Registration.Values, []string{defaultValue}) {
				t.Fatalf("empty override did not remain empty: %+v", resolved)
			}
		})
	}
}
