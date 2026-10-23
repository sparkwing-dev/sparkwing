package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestStoredShortSecretsStayEncryptedAndClassified(t *testing.T) {
	cipher, err := secrets.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"abc", "ééé", ""} {
		sealed, err := SealSecret(cipher, "team", Secret{Name: "TOKEN", Value: value, Masked: true, Pipeline: "deploy"})
		if err != nil {
			t.Fatal(err)
		}
		row := sealed.Row()
		plain, err := cipher.OpenBound(secrets.PurposeSecret, "team", row.Name, row.Pipeline, row.Shared, row.Masked, row.Value)
		if err != nil || plain != value || row.Value == value || !row.Masked {
			t.Fatalf("short secret lost protection: %+v %v", row, err)
		}
		if _, err := cipher.OpenBound(secrets.PurposeSecret, "other", row.Name, row.Pipeline, row.Shared, row.Masked, row.Value); err == nil {
			t.Fatal("foreign team opened secret")
		}
	}
}

func TestLocalShortSecretAccepted(t *testing.T) {
	if err := validateLocalSecrets([]string{"TOKEN"}, map[string]string{"TOKEN": "abc"}); err != nil {
		t.Fatal(err)
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

func TestStoredSecretAcceptsMaskMarker(t *testing.T) {
	cipher, err := secrets.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := SealSecret(cipher, "team", Secret{Name: "TOKEN", Value: "prefix***suffix", Masked: true})
	if err != nil {
		t.Fatal(err)
	}
	if !sealed.Row().Masked || !secrets.IsBound(sealed.Row().Value) {
		t.Fatal("masked value was not sealed")
	}
}
