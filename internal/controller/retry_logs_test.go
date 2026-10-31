package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestRetryLogUploadsUseTheirAwardedWriter(t *testing.T) {
	for _, cloud := range []bool{false, true} {
		t.Run(fmt.Sprintf("cloud=%v", cloud), func(t *testing.T) {
			for _, outcome := range []string{"success", "failed"} {
				t.Run(outcome, func(t *testing.T) {
					t.Run("while retry runs", func(t *testing.T) { retryLogUploads(t, true, cloud, outcome) })
					t.Run("after last attempt seals", func(t *testing.T) { retryLogUploads(t, false, cloud, outcome) })
				})
			}
		})
	}
}

func retryLogUploads(t *testing.T, early, cloud bool, outcome string) {
	t.Helper()
	e, _, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "http://local", SigningKey: bytes.Repeat([]byte{42}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = objects.Close() })
	h := e.Server.WithDataObjects(objects).Handler()
	settings := runspec.JobExecutionSettings{}
	settings.Retry.Set, settings.Retry.Value = true, 1
	execution, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE nodes SET execution_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, execution)
	claimPath := "/api/v1/runners/claim"
	if cloud {
		tenant := teststore.ForTeam(t, e.Store, "a")
		if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100_000_000, Reference: "retry-logs", CreatedBy: "fixture"}); err != nil {
			t.Fatal(err)
		}
		raw = engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
		claimPath = "/api/v1/launcher/claim"
	}
	response := engineClaimHTTP(t, h, raw, "POST", claimPath, `{"runner_version":"v1","kinds":["node"]}`, http.StatusOK)
	var award controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &award); err != nil || award.Claim == nil {
		t.Fatalf("claim: %v", err)
	}
	claim := award.Claim
	if cloud {
		response = engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/claims/exchange", `{}`, http.StatusOK)
		var exchange controllerwire.ExchangeResponse
		if err := json.Unmarshal(response.Body.Bytes(), &exchange); err != nil || exchange.Claim.Capability == "" {
			t.Fatalf("exchange: %v", err)
		}
		claim = &exchange.Claim
	}
	options := client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{h}}, Bearer: func(context.Context) (string, error) { return claim.Capability, nil }}
	host, err := client.NewNodeHostClient(options, "run", "job")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	open := func(ordinal int64) *logseg.Sink {
		t.Helper()
		principal, err := host.Principal(t.Context())
		if err != nil || principal.Capability.Binding.Attempt != ordinal {
			t.Fatalf("attempt %d principal: %+v %v", ordinal, principal.Capability.Binding, err)
		}
		subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: "a", RunID: "run", Attempt: &runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: ordinal, Generation: claim.Binding.Generation}}
		uploader, err := host.LogUploader(t.Context(), principal, subject)
		if err != nil {
			t.Fatal(err)
		}
		sink, err := logseg.NewSink(t.Context(), uploader, logseg.Options{Subject: subject})
		if err != nil {
			t.Fatal(err)
		}
		if err := sink.Append(fmt.Appendf(nil, "attempt %d output\n", ordinal), "", 0, 0); err != nil {
			t.Fatal(err)
		}
		return sink
	}
	engineAdmitBearer(t, e.Store, claim.Capability)
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, http.StatusOK)
	first := open(1)
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"retry","source_attempt":1,"result":{"outcome":"failed","reason":"retry me"}}`, http.StatusOK)
	second := open(2)
	if early {
		if _, err := first.Close(t.Context(), logseg.End{}); err != nil {
			t.Fatalf("first upload during retry: %v", err)
		}
	}
	report := fmt.Sprintf(`{"to":%q,"cause":"report","source_attempt":2,"result":{"outcome":%q}}`, outcome, outcome)
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/transition", report, http.StatusOK)
	var ownerPrefix string
	if err := teststore.SQL(t, e.Store).QueryRowContext(t.Context(), `SELECT capability_prefix FROM triggers WHERE team='a' AND id='run'`).Scan(&ownerPrefix); err != nil {
		t.Fatal(err)
	}
	owner, err := e.Store.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: ownerPrefix})
	if err != nil {
		t.Fatal(err)
	}
	a := capability.Authorized{Principal: owner, Binding: owner.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}}
	if _, err := e.Finish(t.Context(), a, controllerwire.FinishRequest{}); err != nil {
		t.Fatal(err)
	}
	run, err := teststore.ForTeam(t, e.Store, "a").ReadRun(t.Context(), "run")
	if err != nil || string(run.State) != outcome {
		t.Fatalf("finished run state=%s, want %s: %v", run.State, outcome, err)
	}
	ownerSubject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "a", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogTrigger, Generation: owner.Capability.Binding.Generation}}
	ownerBody, err := json.Marshal(logCommitRequest{Subject: ownerSubject, Final: true})
	if err != nil {
		t.Fatal(err)
	}
	engineClaimHTTP(t, h, nativeClaimPresentation(t, e, a), "POST", "/api/v1/claims/"+owner.Capability.UnitID+"/log-manifest", string(ownerBody), http.StatusOK)
	if _, err := second.Close(t.Context(), logseg.End{}); err != nil {
		t.Fatalf("retry upload: %v", err)
	}
	reader, _ := memberLogSearchReader(t, e.Store, objects)
	if !early {
		page, err := reader.ReadLogs(t.Context(), "run", controllerwire.LogQuery{NoEvents: true})
		if err != nil || page.Complete || len(page.Records) != 1 || page.Records[0].Subject.Attempt.Attempt != 2 {
			t.Fatalf("pending first tail: %+v %v", page, err)
		}
		if _, err := first.Close(t.Context(), logseg.End{}); err != nil {
			t.Fatalf("delayed first upload: %v", err)
		}
	}
	page, err := reader.ReadLogs(t.Context(), "run", controllerwire.LogQuery{NoEvents: true})
	if err != nil || !page.Complete || len(page.Records) != 2 {
		t.Fatalf("finished retry aggregate: %+v %v", page, err)
	}
	for _, ordinal := range []int64{1, 2} {
		if !slices.ContainsFunc(page.Records, func(record controllerwire.LogRecord) bool {
			return record.Subject != nil && record.Subject.Attempt != nil && record.Subject.Attempt.Attempt == ordinal && record.Line == fmt.Sprintf("attempt %d output", ordinal)
		}) {
			t.Fatalf("missing attempt %d output: %+v", ordinal, page.Records)
		}
	}
}
