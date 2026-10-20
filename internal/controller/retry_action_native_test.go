package controller

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func retryActionFixture(t *testing.T, value string, accepted bool) (*serverFixture, client.EngineOptions, string, store.RunRow) {
	t.Helper()
	s, session := credentialSessionServer(t)
	s.WithEngineController(NewEngineController(s.store, EngineController{VersionPolicy: engineVersionPolicy(&engineVersionFake{})}))
	engineSQL(t, s.store, `UPDATE sessions SET scopes='runs.read,runs.control'`)
	input := runspec.Request{
		Version: 1, Team: "a", Pipeline: "ci", Origin: runspec.OriginLocal, Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost,
		Actor:         runspec.Actor{Kind: "account", ID: "original-author"},
		Git:           runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, SHA: strings.Repeat("a", 40), Ref: "refs/heads/main"},
		RequestedArgs: map[string]string{"public": "visible", "secret": value}, SecretArgs: runspec.Optional[[]string]{Set: true, Value: []string{"secret"}},
		Source: runspec.Source{Ref: strings.Repeat("a", 40), Location: runspec.SourceLocation{LocalCheckout: "/private/original-checkout", PipelineDir: ".sparkwing"}},
	}
	sealed, err := runspec.SealRequest(input, runspec.IntakeFacts{RunID: "run", AdmittedAt: time.Unix(1, 0).UTC(), Provenance: assertedProvenance(t, "ci", "refs/heads/main", strings.Repeat("a", 40))})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s.store, `UPDATE runs SET status='failed',finished_at=2,request_json=?,request_hash=? WHERE team='a' AND id='run'`, raw, sealed.Hash)
	if accepted {
		plan := json.RawMessage(`{"jobs":[{"id":"job"}]}`)
		spec, err := runspec.SealSpec(runspec.SealInput{Request: sealed, Plan: plan, Now: time.Unix(2, 0), Resolved: runspec.Resolved{
			Engine: engine.Identity{Version: "v1.2.3"}, Args: sealed.Request.RequestedArgs, SecretArgs: []string{"secret"}, Settings: runspec.ExecutionSettings{Jobs: map[string]runspec.JobExecutionSettings{"job": {}}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		engineSQL(t, s.store, `UPDATE runs SET spec_json=?,spec_hash=?,sealed_at=2,plan_json=?,plan_hash=? WHERE team='a' AND id='run'`, raw, spec.Hash, []byte(plan), spec.PlanHash)
	}
	options, prefix := selectorClientOptions(t, s, session, `{"grants":["runs.read","runs.control"]}`)
	tenant, err := s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	return s, options, prefix, row
}

func TestRetryActionPreservesSealsAndCreatesPendingExecution(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(strconv.FormatBool(accepted), func(t *testing.T) {
			s, options, _, original := retryActionFixture(t, "private-retry-value", accepted)
			control, err := client.NewEngineClient(options)
			if err != nil {
				t.Fatal(err)
			}
			tenant, err := s.store.ForTeam(t.Context(), "a")
			if err != nil {
				t.Fatal(err)
			}
			selector, parent := "1", "run"
			for attempt := range 3 {
				input := controllerwire.RetryRequest{Mode: controllerwire.RetryFailed, IdempotencyKey: "retry-" + strconv.Itoa(attempt)}
				result, err := control.Retry(t.Context(), selector, input)
				if err != nil {
					t.Fatal(err)
				}
				if result.RunID == parent || result.RunID == "" || result.Number != int64(attempt+2) || result.Replayed || result.HereBearer != "" || !reflect.DeepEqual(result.Request, original.Request) {
					t.Fatalf("retry changed the seal or execution identity: %+v", result)
				}
				for _, id := range []string{"run", result.RunID} {
					display, err := control.ReadRun(t.Context(), id)
					if err != nil || display.Inputs.RequestedArgs["secret"] != runspec.RedactedArgValue || display.Inputs.RequestedArgs["public"] != "visible" || accepted && display.Inputs.Args["secret"] != runspec.RedactedArgValue {
						t.Fatalf("public retry inputs lost classification: %+v %v", display.Inputs, err)
					}
				}
				row, err := tenant.ReadRun(t.Context(), result.RunID)
				if err != nil || row.State != store.RunPending || row.StartedAt != nil || row.FinishedAt != nil || row.Lineage.RetryOf != parent || !row.Lineage.Retrier.Set || row.Lineage.Retrier.Value.ID != "member" || !reflect.DeepEqual(row.Request, original.Request) || !reflect.DeepEqual(row.Spec, original.Spec) || !bytes.Equal(row.Plan, original.Plan) {
					t.Fatalf("retry did not retain execution inputs and independent lineage: %+v %v", row, err)
				}
				replay, err := control.Retry(t.Context(), selector, input)
				if err != nil || !replay.Replayed || replay.RunID != result.RunID || replay.Number != result.Number || !reflect.DeepEqual(replay.Request, result.Request) {
					t.Fatalf("retry replay: %+v %v", replay, err)
				}
				var unitKind, unitState string
				if err := teststore.SQL(t, s.store).QueryRow(`SELECT unit_kind,status FROM triggers WHERE team='a' AND id=?`, row.ID).Scan(&unitKind, &unitState); err != nil || unitKind != "trigger" || unitState != "pending" {
					t.Fatalf("retry has no pending executable owner: %s %s %v", unitKind, unitState, err)
				}
				selector, parent = result.RunID, result.RunID
			}
		})
	}
}

func TestComputeLimits_RetriesCountAgainstTheRetryingPrincipal(t *testing.T) {
	s, options, _, _ := retryActionFixture(t, "private-value", false)
	control, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.AsOperator().SetComputeLimits(t.Context(), map[string]int64{store.ComputeLimitRunsPerHour: 1}); err != nil {
		t.Fatal(err)
	}
	input := controllerwire.RetryRequest{Mode: controllerwire.RetryFailed, IdempotencyKey: "first"}
	first, err := control.Retry(t.Context(), "run", input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := control.Retry(t.Context(), "1", input)
	if err != nil || !replay.Replayed || replay.RunID != first.RunID {
		t.Fatalf("replay spent another slot: %+v %v", replay, err)
	}
	input.IdempotencyKey = "second"
	_, err = control.Retry(t.Context(), "run", input)
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.RateLimited {
		t.Fatalf("retry past retrier's cap: %v", err)
	}
	runs, err := s.store.AsOperator().ListRuns(t.Context(), store.RunFilter{Limit: 10})
	if err != nil || len(runs) != 2 {
		t.Fatalf("refused retry left an execution: %d %v", len(runs), err)
	}
}

func TestRetryAliasesReplayCommittedMode(t *testing.T) {
	for _, mode := range []controllerwire.RetryMode{controllerwire.RetryFailed, controllerwire.RetryAll} {
		t.Run(string(mode), func(t *testing.T) {
			s, options, _, source := retryActionFixture(t, "private-value", true)
			control, err := client.NewEngineClient(options)
			if err != nil {
				t.Fatal(err)
			}
			request := controllerwire.RetryRequest{Mode: mode, IdempotencyKey: "retry"}
			first, err := control.Retry(t.Context(), "1", request)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := control.Retry(t.Context(), "run", request)
			if err != nil || !replay.Replayed || replay.RunID != first.RunID || replay.Number != first.Number || replay.HereBearer != "" || !reflect.DeepEqual(replay.Request, source.Request) {
				t.Fatalf("alias changed committed execution: %+v %v", replay, err)
			}
			tenant, err := s.store.ForTeam(t.Context(), "a")
			if err != nil {
				t.Fatal(err)
			}
			row, err := tenant.ReadRun(t.Context(), first.RunID)
			if err != nil || row.Retry == nil || row.Retry.Mode != string(mode) || !reflect.DeepEqual(row.Request, source.Request) || !reflect.DeepEqual(row.Spec, source.Spec) {
				t.Fatalf("selection was not committed outside seals: %+v %v", row, err)
			}
		})
	}
}

func FuzzRetryActionCannotCrossAuthorityOrChangeSource(f *testing.F) {
	for _, selector := range []string{"run", "1", "2", "foreign", "01", "../foreign", "' OR 1=1 --"} {
		for state := range byte(3) {
			f.Add(selector, []byte("private-value"), state)
		}
	}
	f.Fuzz(func(t *testing.T, selector string, value []byte, state byte) {
		if len(selector) > 128 || len(value) > 512 {
			t.Skip()
		}
		secret := "private-value-" + hex.EncodeToString(value)
		s, options, prefix, original := retryActionFixture(t, secret, false)
		engineSQL(t, s.store, `INSERT INTO teams(name,created_at,updated_at) VALUES ('b',1,1)`)
		engineSQL(t, s.store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days,finished_at) SELECT 'b','foreign',2,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days,finished_at FROM runs WHERE team='a' AND id='run'`)
		switch state % 3 {
		case 1:
			engineSQL(t, s.store, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, prefix)
		case 2:
			engineSQL(t, s.store, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
		}
		control, err := client.NewEngineClient(options)
		if err != nil {
			t.Fatal(err)
		}
		result, err := control.Retry(t.Context(), selector, controllerwire.RetryRequest{Mode: controllerwire.RetryFailed, IdempotencyKey: "retry"})
		want := state%3 == 0 && (selector == "run" || selector == "1")
		if (err == nil) != want {
			t.Fatalf("selector %q state %d: %+v %v", selector, state%3, result, err)
		}
		if want && !reflect.DeepEqual(result.Request, original.Request) {
			t.Fatal("retry changed its source seal")
		}
		if !want && strings.Contains(err.Error(), secret) {
			t.Fatal("retry error disclosed private execution inputs")
		}
		wantRows := 1
		if want {
			wantRows++
		}
		var rows, foreign int
		if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM runs WHERE team='a'`).Scan(&rows); err != nil || rows != wantRows {
			t.Fatalf("retry committed after refusal: %d %v", rows, err)
		}
		if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM runs WHERE team='b'`).Scan(&foreign); err != nil || foreign != 1 {
			t.Fatalf("retry wrote into another team: %d %v", foreign, err)
		}
	})
}

func TestRetryActionRechecksAuthorityBeforeReplay(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(strconv.FormatBool(revoke), func(t *testing.T) {
			s, options, prefix, _ := retryActionFixture(t, "private-value", false)
			control, err := client.NewEngineClient(options)
			if err != nil {
				t.Fatal(err)
			}
			input := controllerwire.RetryRequest{Mode: controllerwire.RetryFailed, IdempotencyKey: "retry"}
			if _, err := control.Retry(t.Context(), "run", input); err != nil {
				t.Fatal(err)
			}
			if revoke {
				engineSQL(t, s.store, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, prefix)
			} else {
				engineSQL(t, s.store, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
			}
			if _, err := control.Retry(t.Context(), "1", input); err == nil {
				t.Fatal("retry replay survived removal of its current authority")
			}
			var count int
			if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM runs WHERE team='a'`).Scan(&count); err != nil || count != 2 {
				t.Fatalf("dead retry authority wrote another execution: %d %v", count, err)
			}
		})
	}
}

func TestRetryActionUnknownRunAndRemovedRequestShapes(t *testing.T) {
	s, options, _, _ := retryActionFixture(t, "private-value", false)
	control, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = control.Retry(t.Context(), "missing", controllerwire.RetryRequest{Mode: controllerwire.RetryAll, IdempotencyKey: "missing"})
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.NotFound {
		t.Fatalf("missing run: %v", err)
	}
	raw, err := options.Bearer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"", `{}`, `{"full":true}`, `{"mode":"unknown"}`} {
		r := httptest.NewRequest("POST", "/api/v1/runs/"+url.PathEscape("run")+"/retry", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+raw)
		r.Header.Set("Idempotency-Key", "invalid")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("removed retry body %q: HTTP %d %s", body, w.Code, w.Body)
		}
	}
}

func FuzzRetryActionValidatesBeforeAdmission(f *testing.F) {
	for _, mode := range []string{"failed", "all", "", "unknown"} {
		for _, key := range []string{"retry", ""} {
			f.Add(mode, key)
		}
	}
	f.Fuzz(func(t *testing.T, mode, key string) {
		if len(mode) > 128 || len(key) > 128 {
			t.Skip()
		}
		s, options, _, _ := retryActionFixture(t, "private-value", false)
		raw, err := options.Bearer(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(controllerwire.RetryRequest{Mode: controllerwire.RetryMode(mode), IdempotencyKey: key})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/api/v1/runs/run/retry", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		want := (mode == "failed" || mode == "all") && key != ""
		if (w.Code == http.StatusOK) != want || !want && w.Code != http.StatusBadRequest {
			t.Fatalf("selection mode=%q key=%q: HTTP %d", mode, key, w.Code)
		}
		rows := 1
		if want {
			rows++
		}
		var count int
		if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM runs WHERE team='a'`).Scan(&count); err != nil || count != rows {
			t.Fatalf("invalid retry committed: %d %v", count, err)
		}
	})
}
