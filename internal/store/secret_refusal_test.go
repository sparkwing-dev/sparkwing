package store

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type refusalCipher struct{ called bool }

func (c *refusalCipher) SealBound(secrets.Purpose, string, string, string, bool, bool, string) (string, error) {
	c.called = true
	return "", nil
}

func TestStoredSecretRefusesUnmaskableValueBeforeEncryption(t *testing.T) {
	for _, value := range []string{"abc", "prefix***suffix"} {
		cipher := &refusalCipher{}
		_, err := SealSecret(cipher, "team", Secret{Name: "TOKEN", Value: value, Masked: true})
		if err == nil || !strings.Contains(err.Error(), "plain config variable") || cipher.called {
			t.Fatalf("%q: refusal %v, encryption called %t", value, err, cipher.called)
		}
	}
}

func TestLocalSecretRefusesUnmaskableValue(t *testing.T) {
	err := validateLocalSecrets([]string{"TOKEN"}, map[string]string{"TOKEN": "abc"})
	if err == nil || !strings.Contains(err.Error(), "plain config variable") {
		t.Fatalf("expected plain-config refusal, got %v", err)
	}
}

func TestPlainConfigValuesCanBeShortOrContainMaskMarker(t *testing.T) {
	cipher, err := secrets.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"abc", "prefix***suffix", ""} {
		sealed, err := SealSecret(cipher, "team", Secret{Name: "SETTING", Value: value, Masked: false})
		if err != nil {
			t.Fatalf("plain config %q refused: %v", value, err)
		}
		if sealed.Row().Masked || !secrets.IsBound(sealed.Row().Value) {
			t.Fatal("plain config classification changed")
		}
	}
}
