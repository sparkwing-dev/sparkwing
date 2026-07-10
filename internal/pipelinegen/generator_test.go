package pipelinegen

import (
	"context"
	"strings"
	"testing"
)

func TestCommandPromptPrependsRegistrationContract(t *testing.T) {
	p := commandPrompt(Spec{Name: "minimal", Entrypoint: "GenMinimal", Prompt: "log a message"})
	if !strings.Contains(p, `"minimal"`) {
		t.Errorf("prompt should name the registration name: %q", p)
	}
	if !strings.Contains(p, "GenMinimal") {
		t.Errorf("prompt should name the entrypoint struct: %q", p)
	}
	if !strings.Contains(p, "log a message") {
		t.Errorf("prompt should carry the spec body: %q", p)
	}
}

func TestCommandGeneratorReturnsStdoutAndCarriesContract(t *testing.T) {
	gen := CommandGenerator{Argv: []string{"cat"}}
	out, err := gen.Generate(context.Background(), Spec{Name: "minimal", Entrypoint: "GenMinimal", Prompt: "body"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "GenMinimal") || !strings.Contains(out, "body") {
		t.Errorf("generator output missing prompt payload: %q", out)
	}
}

func TestCommandGeneratorEmptyOutputIsError(t *testing.T) {
	gen := CommandGenerator{Argv: []string{"true"}}
	if _, err := gen.Generate(context.Background(), Spec{Name: "x", Entrypoint: "X", Prompt: "p"}); err == nil {
		t.Error("expected an error when the generator produces no output")
	}
}

func TestCommandGeneratorEmptyArgvIsError(t *testing.T) {
	if _, err := (CommandGenerator{}).Generate(context.Background(), Spec{Name: "x", Entrypoint: "X", Prompt: "p"}); err == nil {
		t.Error("expected an error for an empty argv")
	}
}
