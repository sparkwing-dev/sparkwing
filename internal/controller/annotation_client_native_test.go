package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestAnnotationClientAcknowledgesCommittedNativeWrite(t *testing.T) {
	for _, selector := range []string{"run", "1"} {
		t.Run(selector, func(t *testing.T) {
			engine, _ := engineRuntimeStore(t)
			authorized := engineRuntimeClaim(t, engine, capability.UnitNode)
			raw := nativeClaimPresentation(t, engine, authorized)
			server := New(engine.Store, nil)
			engineAdmitBearer(t, engine.Store, raw)
			engineClaimHTTP(t, server.Handler(), raw, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
			review, err := client.NewRunReview(client.EngineOptions{
				URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{server.Handler()}},
				Bearer: func(context.Context) (string, error) { return raw, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			annotation := controllerwire.Annotation{Level: "info", Message: "recorded once", File: "source.go", Line: 7}
			writeErr := review.AppendAnnotation(t.Context(), selector, "job", controllerwire.AnnotationWrite{Annotation: annotation})
			var body []byte
			if err := teststore.SQL(t, engine.Store).QueryRow(`SELECT annotations_json FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`).Scan(&body); err != nil {
				t.Fatal(err)
			}
			var annotations []controllerwire.Annotation
			if err := json.Unmarshal(body, &annotations); err != nil || len(annotations) != 1 || annotations[0] != annotation {
				t.Fatalf("committed annotations: %s %v", body, err)
			}
			if writeErr != nil {
				t.Fatalf("client rejected committed annotation: %v", writeErr)
			}
		})
	}
}

func FuzzMetadataNumericSelectorsRetainJobAuthority(f *testing.F) {
	for _, selector := range []string{"run", "1", "foreign", "2", "01", "+1", "../foreign", "1?team=b"} {
		for state := range byte(3) {
			f.Add(selector, state)
		}
	}
	f.Fuzz(func(t *testing.T, selector string, state byte) {
		if len(selector) > 512 {
			t.Skip()
		}
		engine, _ := engineRuntimeStore(t)
		authorized := engineRuntimeClaim(t, engine, capability.UnitNode)
		raw := nativeClaimPresentation(t, engine, authorized)
		server := New(engine.Store, nil)
		engineAdmitBearer(t, engine.Store, raw)
		engineClaimHTTP(t, server.Handler(), raw, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
		engineClaimHTTP(t, server.Handler(), raw, "POST", "/api/v1/runs/run/jobs/job/steps/start", `{"step_id":"compile","state":"running","source_attempt":1}`, 200)
		engineSQL(t, engine.Store, `INSERT INTO teams(name,created_at,updated_at) VALUES ('b',1,1)`)
		engineSQL(t, engine.Store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) SELECT 'b','foreign',2,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`)
		switch state % 3 {
		case 1:
			engineSQL(t, engine.Store, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, authorized.Principal.Capability.Prefix)
		case 2:
			engineSQL(t, engine.Store, `UPDATE nodes SET generation=generation+1 WHERE team='a' AND run_id='run' AND node_id='job'`)
		}
		wantWrite := state%3 == 0 && (selector == "run" || selector == "1")
		for _, operation := range []struct{ suffix, body string }{
			{"annotations", `{"level":"info","message":"masked note"}`},
			{"summary", `{"markdown":"masked summary"}`},
			{"steps/annotations", `{"step_id":"compile","level":"info","message":"masked note"}`},
			{"steps/summary", `{"step_id":"compile","markdown":"masked summary"}`},
		} {
			request := httptest.NewRequest("POST", "/api/v1/runs/"+url.PathEscape(selector)+"/jobs/job/"+operation.suffix, strings.NewReader(operation.body))
			request.Header.Set("Authorization", "Bearer "+raw)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if (response.Code == http.StatusNoContent) != wantWrite || !wantWrite && (response.Code < 300 || response.Code >= 500) {
				t.Fatalf("metadata selector %q state %d %s: HTTP %d %s", selector, state, operation.suffix, response.Code, response.Body)
			}
		}
		var annotations, summaries int
		err := teststore.SQL(t, engine.Store).QueryRow(`SELECT annotation_count,(SELECT COUNT(*) FROM nodes WHERE summary<>'')+(SELECT COUNT(*) FROM node_steps WHERE summary<>'') FROM runs WHERE team='a' AND id='run'`).Scan(&annotations, &summaries)
		want := 0
		if wantWrite {
			want = 2
		}
		if err != nil || annotations != want || summaries != want {
			t.Fatalf("metadata authority escaped: annotations=%d summaries=%d want=%d %v", annotations, summaries, want, err)
		}
	})
}

func TestNativeProcessReportingCommitsJobAndStepMetadata(t *testing.T) {
	engine, _ := engineRuntimeStore(t)
	authorized := engineRuntimeClaim(t, engine, capability.UnitNode)
	raw := nativeClaimPresentation(t, engine, authorized)
	server := New(engine.Store, nil)
	engineAdmitBearer(t, engine.Store, raw)
	engineClaimHTTP(t, server.Handler(), raw, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	engineClaimHTTP(t, server.Handler(), raw, "POST", "/api/v1/runs/run/jobs/job/steps/start", `{"step_id":"compile","state":"running","source_attempt":1}`, 200)
	host, err := client.NewNodeHostClient(client.EngineOptions{
		URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{server.Handler()}},
		Bearer: func(context.Context) (string, error) { return raw, nil },
	}, "run", "job")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	principal, err := host.Principal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	process, err := host.ProcessClient(t.Context(), principal, "job")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"", "compile"} {
		annotation := nodeproto.AnnotationRequest{Level: "info", Message: "masked note", File: "source.go", Line: 7, Step: step}
		if err := process.Annotation(t.Context(), annotation); err != nil {
			t.Fatal(err)
		}
		if err := process.Summary(t.Context(), nodeproto.SummaryRequest{Markdown: "masked summary", Step: step}); err != nil {
			t.Fatal(err)
		}
		query := `SELECT annotations_json,summary FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`
		if step != "" {
			query = `SELECT annotations_json,summary FROM node_steps WHERE team='a' AND run_id='run' AND node_id='job' AND attempt=1 AND step_id='compile'`
		}
		var body []byte
		var summary string
		if err := teststore.SQL(t, engine.Store).QueryRow(query).Scan(&body, &summary); err != nil {
			t.Fatal(err)
		}
		var annotations []controllerwire.Annotation
		if err := json.Unmarshal(body, &annotations); err != nil || len(annotations) != 1 || annotations[0].Message != annotation.Message || annotations[0].Line != annotation.Line || annotations[0].File != annotation.File || summary != "masked summary" {
			t.Fatalf("metadata step %q: %s %q %v", step, body, summary, err)
		}
	}
}
