package runspec

import (
	"errors"
	"strings"
	"testing"

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
