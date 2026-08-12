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

// SecretArgNames is the classification the orchestrator records on the
// run so read paths can redact without the schema. It must name every
// secret-declared field whether or not the run supplied one, and must
// skip the non-secret field and the `,extra` bag (whose keys carry no
// per-key opt-in).
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

// A pipeline with no secret inputs yields nil, so the orchestrator
// writes no classification key and those runs keep the pre-change
// invocation shape byte-for-byte.
func TestRegistration_SecretArgNamesEmptyWhenNoneDeclared(t *testing.T) {
	Register[noSecretInputs]("no-secret-names-fixture", func() Pipeline[noSecretInputs] {
		return noSecretPipe{}
	})
	reg, _ := Lookup("no-secret-names-fixture")
	if got := reg.SecretArgNames(); got != nil {
		t.Errorf("SecretArgNames = %v, want nil", got)
	}
}
