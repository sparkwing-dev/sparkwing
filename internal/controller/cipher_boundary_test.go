package controller_test

import (
	"bytes"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type unboundRowCipher struct{ calls int }

func (c *unboundRowCipher) Seal(value string) (string, error) {
	c.calls++
	return value, nil
}

func (c *unboundRowCipher) Open(value string) (string, error) {
	c.calls++
	return value, nil
}

func TestSecretRowHelpersNeverUseUnboundCipherProperty(t *testing.T) {
	property := func(value, team, name, pipeline string, shared, masked bool) bool {
		cipher := &unboundRowCipher{}
		row := &store.Secret{Name: name, Pipeline: pipeline, Shared: shared, Masked: masked, Value: value}
		sealed, sealErr := controller.SealSecretValue(cipher, store.Team(team), row, value)
		opened, openErr := controller.OpenSecretValue(cipher, store.Team(team), row)
		return sealErr != nil && openErr != nil && sealed == "" && opened == "" && cipher.calls == 0
	}
	if err := quick.Check(property, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSecretRowHelpersPreserveBoundEnvelopes(t *testing.T) {
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{41}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	row := &store.Secret{Name: "TOKEN", Pipeline: "build", Shared: true, Masked: true}
	row.Value, err = controller.SealSecretValue(cipher, "team", row, "private-value")
	if err != nil {
		t.Fatal(err)
	}
	value, err := controller.OpenSecretValue(cipher, "team", row)
	if err != nil || value != "private-value" || !secrets.IsBound(row.Value) {
		t.Fatalf("bound round trip failed: %v", err)
	}
	for _, cipher := range []controller.Cipher{nil, &unboundRowCipher{}} {
		if value, err := controller.OpenSecretValue(cipher, "team", row); err == nil || value != "" {
			t.Fatal("missing bound cipher opened a stored value")
		}
	}
}
