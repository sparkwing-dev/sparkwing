package sparkwing

import (
	"context"
	"reflect"
	"testing"
)

type secretNamesInputs struct {
	Token   string            `flag:"token" secret:"true"`
	APIKey  string            `flag:"api-key" secret:"true" default:"fallback"`
	Visible string            `flag:"visible"`
	Extra   map[string]string `flag:",extra"`
}

type secretNamesPipe struct{}

func (secretNamesPipe) Plan(_ context.Context, _ *Plan, _ secretNamesInputs, _ RunContext) error {
	return nil
}

func TestRegistration_SecretArgNames(t *testing.T) {
	Register[secretNamesInputs]("secret-names-fixture", func() Pipeline[secretNamesInputs] {
		return secretNamesPipe{}
	})
	reg, ok := Lookup("secret-names-fixture")
	if !ok {
		t.Fatal("fixture not registered")
	}
	got := reg.SecretArgNames()
	want := []string{"api-key", "token"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SecretArgNames = %v, want %v", got, want)
	}
}

type noSecretInputs struct {
	Visible string `flag:"visible"`
}

type noSecretPipe struct{}

func (noSecretPipe) Plan(_ context.Context, _ *Plan, _ noSecretInputs, _ RunContext) error {
	return nil
}

func TestRegistration_SecretArgNamesEmptyWhenNoneDeclared(t *testing.T) {
	Register[noSecretInputs]("no-secret-names-fixture", func() Pipeline[noSecretInputs] {
		return noSecretPipe{}
	})
	reg, _ := Lookup("no-secret-names-fixture")
	if got := reg.SecretArgNames(); got != nil {
		t.Errorf("SecretArgNames = %v, want nil", got)
	}
}
