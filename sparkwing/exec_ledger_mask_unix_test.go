//go:build unix

package sparkwing_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// A step's session ledger record outlives its run's masker: a sweep copies
// the record's command line into a stray_session_reaped event. A secret
// passed on that command line must already be masked in the record, which
// the step reads once the parent has written it.
func TestStepLedgerRecordMasksSecretsOnTheCommandLine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_RUN_ID", "run-1")
	t.Setenv("SPARKWING_NODE_ID", "deploy")
	const secret = "hunter2-deploy-token"
	masker := secrets.NewMasker()
	masker.Register(secret)
	ctx := secrets.WithMasker(context.Background(), masker)

	script := `for i in $(seq 500); do ls "$SPARKWING_HOME"/sessions/run-1/deploy/*.json >/dev/null 2>&1 && break; sleep 0.01; done; ` +
		`cat "$SPARKWING_HOME"/sessions/run-1/deploy/*.json; : ` + secret
	out, err := sparkwing.Bash(ctx, script).String()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"command"`) {
		t.Fatalf("the step saw no ledger record of itself: %q", out)
	}
	if strings.Contains(out, secret) || !strings.Contains(out, "***") {
		t.Fatalf("ledger record = %s, want the secret masked", out)
	}
}
