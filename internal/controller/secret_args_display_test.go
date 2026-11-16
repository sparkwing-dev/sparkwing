package controller_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
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
	var failure hosterr.Envelope
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
	assertRedactedResponse(t, "latest", secretArgBody(t, f, "/api/v1/pipelines/deploy/latest?from_any_run=true"), ctlSecretValue, true)
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
		for _, path := range []string{"/api/v1/runs", "/api/v1/runs/" + run, "/api/v1/runs/" + run + "/attempts", "/api/v1/pipelines/deploy/latest?from_any_run=true"} {
			target := path + query
			if strings.Contains(path, "?") && query != "" {
				target = path + "&" + strings.TrimPrefix(query, "?")
			}
			assertRedactedResponse(t, target, secretArgBody(t, f, target), private, classified)
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
	property := func(value []byte, classified bool) bool {
		f := newBrowserFixture(t, nil, fixtureStorePolicies(store.Policies{Intake: receiptPolicy{}}))
		private := "private-argument-" + hex.EncodeToString(value)
		admitted := secretArgRun(t, f, private, classified)
		checkSecretArgViews(t, f, admitted.RunID, private, classified)
		stored, err := teststore.ForTeam(t, f.st, store.DefaultTeam).ReadRun(t.Context(), admitted.RunID)
		return err == nil && reflect.DeepEqual(stored.Request, admitted.Request) && !strings.Contains(f.logs.String(), private)
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 24})); err != nil {
		t.Fatal(err)
	}
}

func TestSecretArgs_CorruptEnvelopeFailsOnlyItsOwnRun(t *testing.T) {
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{7}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	f := newBrowserFixture(t, nil, fixtureStorePolicies(store.Policies{Intake: receiptPolicy{}, RunEnvironment: cipher}))
	healthy := secretArgRun(t, f, ctlSecretValue, true)
	damaged := secretArgRun(t, f, ctlSecretValue, true)
	var raw []byte
	if err := teststore.SQL(t, f.st).QueryRow(`SELECT request_json FROM runs WHERE id=$1`, damaged.RunID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	envelope, _ := doc["sealed_arguments"].(string)
	if len(envelope) < 20 {
		t.Fatalf("damaged run carries no argument envelope: %s", raw)
	}
	flipped := []byte(envelope)
	flipped[len(flipped)/2] ^= 1
	doc["sealed_arguments"] = string(flipped)
	if raw, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, f.st).Exec(`UPDATE runs SET request_json=$1 WHERE id=$2`, raw, damaged.RunID); err != nil {
		t.Fatal(err)
	}

	list := secretArgBody(t, f, "/api/v1/runs")
	if !strings.Contains(list, healthy.RunID) || !strings.Contains(list, damaged.RunID) || strings.Contains(list, ctlSecretValue) || !strings.Contains(list, `"`+ctlVisibleValue+`"`) || !strings.Contains(list, `"(unavailable)"`) {
		t.Fatalf("one damaged envelope broke or leaked the run list: %s", list)
	}
	if body := secretArgBody(t, f, "/api/v1/runs/"+healthy.RunID); strings.Contains(body, ctlSecretValue) {
		t.Fatalf("healthy run detail leaked its secret: %s", body)
	}
	r := f.cookieRequest(http.MethodGet, "/api/v1/runs/"+damaged.RunID, "")
	r.Header.Set("Accept", "application/json")
	w := f.serve(r)
	if w.Code == http.StatusOK || strings.Contains(w.Body.String(), ctlSecretValue) {
		t.Fatalf("damaged run detail: HTTP %d %s", w.Code, w.Body)
	}
}
