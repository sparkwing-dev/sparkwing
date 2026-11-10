package settings

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestDeclarationVisitorIncludesOverriddenProfile(t *testing.T) {
	declarations := runspec.CheckoutDeclarations{}
	declarations.Defaults.Profile.Value = "private-profile"
	declarations.Pipeline.Profile.Value = "safe"
	sentinel := errors.New("matched retained profile")
	err := VisitDeclarationIdentifiers(declarations, func(value, path string) error {
		if value == "private-profile" {
			if path != "declarations.defaults.profile" {
				t.Fatal("wrong diagnostic path")
			}
			return sentinel
		}
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatal("retained overridden profile was not visited")
	}
}
