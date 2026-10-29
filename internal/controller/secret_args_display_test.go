package controller_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

const (
	ctlSecretValue  = "s3cr3t-token-value"
	ctlVisibleValue = "prod"
)

func secretArgRun(t *testing.T, f *browserFixture, private string, classified bool) store.AdmissionResult {
	t.Helper()
	principal, err := f.st.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(f.token)))
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := teststore.ForTeam(t, f.st, store.DefaultTeam).AdmitRun(t.Context(), store.IntakeRequest{
		Authority: principal.Authority,
		Request: runspec.Request{
			Version: 1, Team: string(store.DefaultTeam), Pipeline: "deploy", Mode: runspec.IntakeExecute,
			Source:   runspec.Source{Ref: strings.Repeat("a", 40)},
			Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, Ref: "main", SHA: strings.Repeat("a", 40)},
			RequestedArgs: map[string]string{"token": private, "env": ctlVisibleValue},
			SecretArgs:    runspec.Optional[[]string]{Set: classified, Value: []string{"token"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedRunFinish(f.st, t.Context(), store.DefaultTeam, admitted.RunID, "success", ""); err != nil {
		t.Fatal(err)
	}
	return admitted
}

func secretArgBody(t *testing.T, f *browserFixture, path string) string {
	t.Helper()
	r := f.cookieRequest(http.MethodGet, path, "")
	r.Header.Set("Accept", "application/json")
	w := f.serve(r)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET %s: HTTP %d %s", path, w.Code, w.Body)
	}
	return w.Body.String()
}

func assertRedactedResponse(t *testing.T, surface, body, private string, classified bool) {
	t.Helper()
	if strings.Contains(body, private) || !strings.Contains(body, runspec.RedactedArgValue) || strings.Contains(body, `"`+ctlVisibleValue+`"`) != classified {
		t.Fatalf("%s did not retain classification: %s", surface, body)
	}
}

func TestSecretArgs_ControllerListRunsRedacts(t *testing.T) {
	f := newBrowserFixture(t, nil, fixtureStorePolicies(store.Policies{Intake: receiptPolicy{}}))
	secretArgRun(t, f, ctlSecretValue, true)
	assertRedactedResponse(t, "list", secretArgBody(t, f, "/api/v1/runs"), ctlSecretValue, true)
}

func TestSecretArgs_ControllerReceiptRedacts(t *testing.T) {
	f := newBrowserFixture(t, nil, fixtureStorePolicies(store.Policies{Intake: receiptPolicy{}}))
	admitted := secretArgRun(t, f, ctlSecretValue, true)
	assertPrivateReceipt(t, secretArgBody(t, f, "/api/v1/runs/"+admitted.RunID+"/receipt"), ctlSecretValue)
}

func assertPrivateReceipt(t *testing.T, body, private string) {
	t.Helper()
	var rec controllerwire.Receipt
	if err := json.Unmarshal([]byte(body), &rec); err != nil || rec.Identity.InputsHash != "" || rec.ReceiptSHA == "" || strings.Contains(body, private) || strings.Contains(body, "offline-oracle") || strings.Contains(body, `"invocation"`) {
		t.Fatalf("receipt disclosed private inputs or a hash oracle: %s (%v)", body, err)
	}
}

func TestSecretArgs_ControllerRejectsOlderWriterInputHash(t *testing.T) {
	f := newBrowserFixture(t, nil, fixtureStorePolicies(store.Policies{Intake: receiptPolicy{}}))
	w := f.serve(f.cookieRequest(http.MethodPost, "/api/v1/runs", `{"id":"old-writer","pipeline":"deploy","invocation":{"inputs_hash":"sha256:offline-oracle","args":{"token":"s3cr3t-token-value"}}}`))
	var failure apierr.Envelope
	if w.Code != http.StatusBadRequest || json.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.Error.Code != apierr.BadRequest || strings.Contains(w.Body.String(), ctlSecretValue) || strings.Contains(w.Body.String(), "offline-oracle") {
		t.Fatalf("old writer: HTTP %d %s", w.Code, w.Body)
	}
	var count int
	if err := teststore.SQL(t, f.st).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM runs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("refused old writer inserted %d runs: %v", count, err)
	}
}

func TestSecretArgs_ControllerPipelineLatestRedacts(t *testing.T) {
	f := newBrowserFixture(t, nil, fixtureStorePolicies(store.Policies{Intake: receiptPolicy{}}))
	secretArgRun(t, f, ctlSecretValue, true)
	assertRedactedResponse(t, "latest", secretArgBody(t, f, "/api/v1/pipelines/deploy/latest"), ctlSecretValue, true)
}

func TestSecretArgs_ControllerAttemptsRedacts(t *testing.T) {
	f := newBrowserFixture(t, nil, fixtureStorePolicies(store.Policies{Intake: receiptPolicy{}}))
	admitted := secretArgRun(t, f, ctlSecretValue, true)
	assertRedactedResponse(t, "attempts", secretArgBody(t, f, "/api/v1/runs/"+admitted.RunID+"/attempts"), ctlSecretValue, true)
}

func TestSecretArgs_RetryStillReceivesPlaintext(t *testing.T) {
	f := newBrowserFixture(t, nil, fixtureStorePolicies(store.Policies{Intake: receiptPolicy{}}))
	admitted := secretArgRun(t, f, ctlSecretValue, true)
	w := f.serve(f.cookieRequest(http.MethodPost, "/api/v1/runs/"+admitted.RunID+"/retry", `{"mode":"all","idempotency_key":"private-input-retry"}`))
	var response controllerwire.CreateRunResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.RunID == "" || response.RunID == admitted.RunID {
		t.Fatalf("retry: HTTP %d %s", w.Code, w.Body)
	}
	tenant := teststore.ForTeam(t, f.st, store.DefaultTeam)
	for _, id := range []string{admitted.RunID, response.RunID} {
		row, err := tenant.ReadRun(t.Context(), id)
		if err != nil || row.Request.Request.RequestedArgs["token"] != ctlSecretValue || !row.Request.Request.SecretArgs.Set || len(row.Request.Request.SecretArgs.Value) != 1 || row.Request.Request.SecretArgs.Value[0] != "token" {
			t.Fatalf("retry lost private execution inputs: %v", err)
		}
		if id == response.RunID && row.Lineage.RetryOf != admitted.RunID {
			t.Fatalf("retry source=%s", row.Lineage.RetryOf)
		}
		assertRedactedResponse(t, "run detail", secretArgBody(t, f, "/api/v1/runs/"+id), ctlSecretValue, true)
	}
}

func TestSecretArgs_ExecutionViewDoesNotWidenOtherEndpoints(t *testing.T) {
	f := newBrowserFixture(t, nil, fixtureStorePolicies(store.Policies{Intake: receiptPolicy{}}))
	admitted := secretArgRun(t, f, ctlSecretValue, true)
	checkSecretArgViews(t, f, admitted.RunID, ctlSecretValue, true)
}

func checkSecretArgViews(t *testing.T, f *browserFixture, run, private string, classified bool) {
	t.Helper()
	for _, query := range []string{"", "?include=secret_values"} {
		for _, path := range []string{"/api/v1/runs", "/api/v1/runs/" + run, "/api/v1/runs/" + run + "/attempts", "/api/v1/pipelines/deploy/latest"} {
			assertRedactedResponse(t, path+query, secretArgBody(t, f, path+query), private, classified)
		}
		assertPrivateReceipt(t, secretArgBody(t, f, "/api/v1/runs/"+run+"/receipt"+query), private)
		legacy := f.serve(f.cookieRequest(http.MethodGet, "/api/v1/runs"+query, ""))
		if legacy.Code != http.StatusOK {
			t.Fatalf("default run list: HTTP %d %s", legacy.Code, legacy.Body)
		}
		assertRedactedResponse(t, "default run list", legacy.Body.String(), private, classified)
	}
}

func TestSecretArgsPublicReadsProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 2.3s measured in the short class; the full suite runs it")
	}
	property := func(value []byte, classified bool) bool {
		f := newBrowserFixture(t, nil, fixtureStorePolicies(store.Policies{Intake: receiptPolicy{}}))
		private := "private-argument-" + hex.EncodeToString(value)
		admitted := secretArgRun(t, f, private, classified)
		checkSecretArgViews(t, f, admitted.RunID, private, classified)
		stored, err := teststore.ForTeam(t, f.st, store.DefaultTeam).ReadRun(t.Context(), admitted.RunID)
		return err == nil && reflect.DeepEqual(stored.Request, admitted.Request) && !strings.Contains(f.logs.String(), private)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
}
