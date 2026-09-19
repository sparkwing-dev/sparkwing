package sparkwing_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func grantedCtx(ctx context.Context) context.Context {
	return sparkwing.Grant(ctx)
}

func refusalFrom(call func()) (msg string, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			msg = fmt.Sprint(r)
		}
	}()
	call()
	return "", false
}

func TestBash_RefusesAContextCarryingNoGrant(t *testing.T) {
	msg, panicked := refusalFrom(func() { _, _ = sparkwing.Bash(context.Background(), "true").Run() })
	if !panicked {
		t.Fatal("Bash ran on a context carrying no grant")
	}
	if !strings.Contains(msg, "sparkwing.Bash") {
		t.Errorf("refusal = %q, want it to name sparkwing.Bash so the author finds the call", msg)
	}
}

func TestExec_RefusesAContextCarryingNoGrant(t *testing.T) {
	msg, panicked := refusalFrom(func() { _, _ = sparkwing.Exec(context.Background(), "true").Capture() })
	if !panicked {
		t.Fatal("Exec ran on a context carrying no grant")
	}
	if !strings.Contains(msg, "sparkwing.Exec") {
		t.Errorf("refusal = %q, want it to name sparkwing.Exec", msg)
	}
}

func TestBash_RunsOnAGrantedContext(t *testing.T) {
	if _, panicked := refusalFrom(func() {
		_, _ = sparkwing.Bash(grantedCtx(context.Background()), "true").Run()
	}); panicked {
		t.Fatal("Bash refused a granted context")
	}
}
