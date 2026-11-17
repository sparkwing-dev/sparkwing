package sdkserve

import (
	"context"
	"errors"
	"strings"
	"testing"

	nodeproto "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestPublicExecutionContexts(t *testing.T) {
	if Planning(t.Context()) {
		t.Fatal("ordinary context is planning")
	}
	ctx := DeclarationContext(t.Context())
	if !Planning(ctx) {
		t.Fatal("planning marker lost")
	}
	RequireExecution(t.Context(), "allowed")
	t.Run("planning refuses side effects", func(t *testing.T) {
		defer func() {
			r := recover()
			if r == nil || !strings.Contains(r.(string), "forbidden called inside Pipeline.Plan()") {
				t.Fatalf("panic=%v", r)
			}
		}()
		RequireExecution(ctx, "forbidden")
	})
	if HelperStep(t.Context()) != "" || HelperStep(WithHelperStep(t.Context(), "build")) != "build" {
		t.Fatal("step binding lost")
	}
	if SecretIsVariable(t.Context()) || !SecretIsVariable(WithSecretClass(t.Context(), true)) {
		t.Fatal("variable classification lost")
	}
	if _, bound := HostExec(t.Context()); bound {
		t.Fatal("unbound host accepted")
	}
	want := errors.New("command failed")
	var fn HostExecFunc = func(_ context.Context, req nodeproto.ExecRequest) (HostExecResult, error) {
		return HostExecResult{Stdout: []byte(req.ID)}, want
	}
	run, bound := HostExec(WithHostExec(t.Context(), fn))
	if !bound {
		t.Fatal("host binding missing")
	}
	result, err := run(t.Context(), nodeproto.ExecRequest{ID: "command"})
	if !errors.Is(err, want) || string(result.Stdout) != "command" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestPublicHelperErrorsAndMissingHost(t *testing.T) {
	err := &HelperError{Code: "missing", Message: "safe message"}
	if err.Error() != "safe message" || !err.Is(errors.New("missing")) || err.Is(errors.New("other")) {
		t.Fatalf("error=%+v", err)
	}
	if got := Helper(t.Context(), "git.Fetch", []any{"repo"}, nil); got == nil || !strings.Contains(got.Error(), "host helper service unavailable") {
		t.Fatalf("error=%v", got)
	}
}

func TestPublicMainMissingPipes(t *testing.T) {
	if got := Main(Config{Operations: &wireOperations{}}); got != 1 {
		t.Fatalf("exit=%d", got)
	}
}
