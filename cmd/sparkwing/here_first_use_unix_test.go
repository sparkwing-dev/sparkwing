//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/sparkwing/testsupport/testhome"
)

func TestHereTriggerFreshHomeExecutesFirstJob(t *testing.T) {
	if testing.Short() {
		t.Skip("executes an SDK job through a remote controller")
	}
	f := cloudDispatchSetup(t, true)
	testhome.Set(t, testhome.TempDir(t))
	home := testhome.TempDir(t)
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", testhome.TempDir(t))
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(home, "secrets.key"))
	offlineStopDaemon(t, home)
	if _, err := wingdclient.QueryLocalController(t.Context(), home); !errors.Is(err, wingdclient.ErrNoDaemon) {
		t.Fatalf("expected no admission controller in the fresh home: %v", err)
	}
	f.request.Request.Options.Runner = initialTriggerRequest(true).Request.Options.Runner
	f.request.Request.Reason = "inspect first job"
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	var out bytes.Buffer
	err := runTriggerCommandArgs(ctx, []string{"deploy-prod", "--profile=cloud", "--runner=here", "--reason=inspect first job", "-o=json"}, &out,
		func(context.Context, triggerFlags) (controllerwire.CreateRunRequest, error) { return f.request, nil }, controllerOptions)
	if err != nil {
		t.Fatalf("fresh-home here trigger: %v\n%s", err, &out)
	}
	var receipt triggerReceipt
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil || receipt.RunID == "" {
		t.Fatalf("here receipt: %s: %v", &out, err)
	}
	detail, err := f.member.ReadRun(ctx, receipt.RunID)
	if err != nil || detail.Status != "success" || len(detail.Jobs) != 1 || detail.Jobs[0].Outcome != "success" {
		t.Fatalf("first here job did not succeed: %+v: %v", detail, err)
	}
}
