package controller

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestPublicNumericSelectorsReadAndCommitCanonicalRun(t *testing.T) {
	s, session := pauseHTTPFixture(t)
	options, _ := selectorClientOptions(t, s, session, `{"grants":["runs.read","runs.control"]}`)
	reader, err := client.NewRunReader(options)
	if err != nil {
		t.Fatal(err)
	}
	run, err := reader.ReadRun(t.Context(), "1")
	if err != nil || run.ID != "run" || run.Number != 1 {
		t.Fatalf("numeric run: %+v %v", run, err)
	}
	if _, err := reader.ReadSteps(t.Context(), "1"); err != nil {
		t.Fatalf("numeric steps: %v", err)
	}
	bearer, err := options.Bearer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"receipt", "events", "jobs", "jobs/job/attempts", "jobs/job/steps", "jobs/job/metrics", "debug-pauses", "approvals"} {
		engineClaimHTTP(t, s.Handler(), bearer, "GET", "/api/v1/runs/1/"+suffix, "", 200)
	}
	receipt, err := reader.ReadReceipt(t.Context(), "1")
	if err != nil || receipt.RunID != "run" || len(receipt.Steps) != 1 || receipt.Steps[0].JobID != "job" {
		t.Fatalf("numeric receipt: %+v %v", receipt, err)
	}
	control, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Cancel(t.Context(), "1", controllerwire.CancelRequest{Reason: "user_cancelled"}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM runs WHERE team='a' AND id='run' AND cancel_requested_at IS NOT NULL`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("canonical run cancellation: %d %v", count, err)
	}
}
