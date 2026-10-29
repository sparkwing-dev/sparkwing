package controller

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func seedRetryHistory(t *testing.T, s *serverFixture, secret string) {
	t.Helper()
	var raw []byte
	if err := teststore.SQL(t, s.store).QueryRow(`SELECT request_json FROM runs WHERE id='run'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var sealed runspec.SealedRequest
	if err := json.Unmarshal(raw, &sealed); err != nil {
		t.Fatal(err)
	}
	sealed.Request.RequestedArgs = map[string]string{"secret": secret, "public": "visible"}
	sealed.Request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{"secret"}}
	sealed.Request.Source.Location.LocalCheckout = "/private/checkout/" + secret
	sealed.Request.Env.Digest = runspec.Hash(secret)
	raw, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s.store, `UPDATE runs SET request_json=?,created_at=1 WHERE id='run'`, raw)
	for i, retry := range []struct{ id, parent string }{{"second", "run"}, {"sibling", "run"}, {"last", "second"}} {
		lineage, err := json.Marshal(runspec.Lineage{RetryOf: retry.parent})
		if err != nil {
			t.Fatal(err)
		}
		engineSQL(t, s.store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days,retry_of,lineage_json) SELECT team,?,?,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,?,log_retention_class,log_retention_days,?,? FROM runs WHERE id='run'`, retry.id, i+2, i+2, retry.parent, lineage)
	}
}

func TestRetryHistoryUsesPublicProjectionForEverySelector(t *testing.T) {
	s, session := pauseHTTPFixture(t)
	seedRetryHistory(t, s, "private-retry-value")
	options, _ := selectorClientOptions(t, s, session, `{"grants":["runs.read"]}`)
	raw, err := options.Bearer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{"run", "1", "second", "2", "sibling", "3", "last", "4"} {
		w := engineClaimHTTP(t, s.Handler(), raw, "GET", "/api/v1/runs/"+selector+"/attempts", "", 200)
		assertRetryHistoryProjection(t, w.Body.Bytes(), "private-retry-value", []string{"run", "second", "sibling", "last"})
	}
}

func assertRetryHistoryProjection(t *testing.T, body []byte, secret string, ids []string) {
	t.Helper()
	var page controllerwire.RunPage
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != len(ids) || page.Truncated || page.NextCursor != "" {
		t.Fatalf("history contains %d runs, truncated=%t cursor=%q, want %d complete runs", len(page.Runs), page.Truncated, page.NextCursor, len(ids))
	}
	for i, run := range page.Runs {
		if run.ID != ids[i] || run.Team != "a" || run.Inputs.Pipeline != "ci" || run.Inputs.RequestedArgs["secret"] != runspec.RedactedArgValue || run.Inputs.RequestedArgs["public"] != "visible" {
			t.Fatalf("unsafe or incomplete history: %s", body)
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	projected, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]json.RawMessage
	if err := json.Unmarshal(projected, &want); err != nil {
		t.Fatal(err)
	}
	var actualRuns, wantedRuns []map[string]any
	if json.Unmarshal(fields["runs"], &actualRuns) != nil || json.Unmarshal(want["runs"], &wantedRuns) != nil || !reflect.DeepEqual(actualRuns, wantedRuns) || bytes.Contains(body, []byte(secret)) {
		t.Fatalf("history escaped shared public projection: %s", body)
	}
}

func TestRetryHistoryRechecksEachRunBinding(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	owner := engineRuntimeClaim(t, e, capability.UnitTrigger)
	raw := nativeClaimPresentation(t, e, owner)
	s := New(e.Store, nil).WithEngineController(e.EngineController)
	seedRetryHistory(t, &serverFixture{Server: s, store: e.Store, engine: e}, "private-retry-value")
	engineClaimHTTP(t, s.Handler(), raw, "GET", "/api/v1/runs/run/attempts", "", 403)
}

func TestRetryHistoryTraversesLongChainsAndCycles(t *testing.T) {
	s, session := pauseHTTPFixture(t)
	seedRetryHistory(t, s, "private-retry-value")
	ids := []string{"run", "second", "sibling", "last"}
	parent := "last"
	for i := range 270 {
		id := fmt.Sprintf("retry-%d", i)
		lineage, err := json.Marshal(runspec.Lineage{RetryOf: parent})
		if err != nil {
			t.Fatal(err)
		}
		engineSQL(t, s.store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days,retry_of,lineage_json) SELECT team,?,?,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,?,log_retention_class,log_retention_days,?,? FROM runs WHERE id='run'`, id, i+5, i+5, parent, lineage)
		ids = append(ids, id)
		parent = id
	}
	options, _ := selectorClientOptions(t, s, session, `{"grants":["runs.read"]}`)
	raw, err := options.Bearer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, cycle := range []bool{false, true} {
		if cycle {
			engineSQL(t, s.store, `UPDATE runs SET retry_of=? WHERE id='run'`, parent)
		}
		for _, selector := range []string{"run", parent, "274"} {
			w := engineClaimHTTP(t, s.Handler(), raw, "GET", "/api/v1/runs/"+selector+"/attempts", "", 200)
			assertRetryHistoryProjection(t, w.Body.Bytes(), "private-retry-value", ids)
		}
	}
}

func TestRetryHistoryOrdersEqualTimestampsByRunNumber(t *testing.T) {
	s, session := pauseHTTPFixture(t)
	seedRetryHistory(t, s, "private-retry-value")
	engineSQL(t, s.store, `UPDATE runs SET created_at=1`)
	options, _ := selectorClientOptions(t, s, session, `{"grants":["runs.read"]}`)
	raw, err := options.Bearer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	w := engineClaimHTTP(t, s.Handler(), raw, "GET", "/api/v1/runs/last/attempts", "", 200)
	assertRetryHistoryProjection(t, w.Body.Bytes(), "private-retry-value", []string{"run", "second", "sibling", "last"})
}

func FuzzRetryHistoryKeepsPrivateInputsAndTeams(f *testing.F) {
	for _, selector := range []string{"run", "1", "second", "2", "foreign", "5", "01", "../foreign", "' OR 1=1 --"} {
		for state := range byte(4) {
			f.Add(selector, []byte("private-value"), state)
		}
	}
	f.Fuzz(func(t *testing.T, selector string, value []byte, state byte) {
		if len(selector) > 128 || len(value) > 512 {
			t.Skip()
		}
		secret := "private-value-" + hex.EncodeToString(value)
		s, session := pauseHTTPFixture(t)
		seedRetryHistory(t, s, secret)
		engineSQL(t, s.store, `INSERT INTO teams(name,created_at,updated_at) VALUES ('b',1,1)`)
		engineSQL(t, s.store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days,retry_of) SELECT 'b','foreign',5,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days,'run' FROM runs WHERE id='run'`)
		engineSQL(t, s.store, `UPDATE runs SET retry_of='foreign' WHERE id='run'`)
		options, prefix := selectorClientOptions(t, s, session, `{"grants":["runs.read"]}`)
		raw, err := options.Bearer(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		switch state % 4 {
		case 1:
			engineSQL(t, s.store, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, prefix)
		case 2:
			engineSQL(t, s.store, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
		case 3:
			e := newEngineFixture(s.store, EngineController{})
			owner := engineRuntimeClaim(t, e, capability.UnitTrigger)
			raw = nativeClaimPresentation(t, e, owner)
		}
		r := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+url.PathEscape(selector)+"/attempts", nil)
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		wantSuccess := state%4 == 0 && (selector == "run" || selector == "1" || selector == "second" || selector == "2" || selector == "sibling" || selector == "3" || selector == "last" || selector == "4")
		if (w.Code == http.StatusOK) != wantSuccess {
			t.Fatalf("selector %q state %d: HTTP %d %s", selector, state%4, w.Code, w.Body)
		}
		if wantSuccess {
			assertRetryHistoryProjection(t, w.Body.Bytes(), secret, []string{"run", "second", "sibling", "last"})
		}
	})
}
