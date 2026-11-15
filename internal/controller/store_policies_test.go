package controller_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type unboundTestCipher struct{}

func (unboundTestCipher) Seal(s string) (string, error) { return s, nil }
func (unboundTestCipher) Open(s string) (string, error) { return s, nil }

func TestStorePoliciesRefuseRunArgumentsUnderAnUnboundCipher(t *testing.T) {
	policies := controller.New(nil, nil).WithSecretsCipher(unboundTestCipher{}).StorePolicies()
	if policies.RunEnvironment == nil {
		t.Fatal("an unbound cipher left run arguments without a cipher, which stores them in plaintext")
	}
	if _, err := policies.RunEnvironment.SealBound(secrets.PurposeRunArguments, "team", "run", "request", false, true, "value"); err == nil {
		t.Fatal("an unbound cipher sealed run arguments")
	}
	if policies.SessionKeys != nil {
		t.Fatal("an unbound cipher was offered as the session key cipher")
	}
}
