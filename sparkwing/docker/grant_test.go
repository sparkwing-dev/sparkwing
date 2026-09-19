package docker

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

func grantedCtx(ctx context.Context) context.Context {
	return planguard.Grant(ctx)
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

func TestGuardedHelperRefusesAContextCarryingNoGrant(t *testing.T) {
	msg, panicked := refusalFrom(func() { _, _ = ComputeTags(context.Background()) })
	if !panicked {
		t.Fatal("a guarded helper ran on a context carrying no grant")
	}
	if !strings.Contains(msg, "docker.ComputeTags") {
		t.Errorf("refusal = %q, want it to name the helper", msg)
	}
}
