package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type publicationLogReader struct{ controllerwire.LogReader }

func (publicationLogReader) ReadLogs(_ context.Context, run string, _ controllerwire.LogQuery) (controllerwire.LogPage, error) {
	a := runspec.AttemptIdentity{Team: "local", RunID: run, NodeID: "job", Attempt: 1, Generation: 1}
	return controllerwire.LogPage{Complete: true, Records: []controllerwire.LogRecord{{RunID: run, LineNo: 1, Line: "masked local output ***", Subject: &blob.LogSubject{Kind: blob.LogJobAttempt, Team: "local", RunID: run, Attempt: &a}}}}, nil
}

func TestProfilePublicationImportsRecordsAndLogsWithoutClaims(t *testing.T) {
	for _, outcome := range []string{"success", "failed"} {
		t.Run(outcome, func(t *testing.T) { profilePublication(t, outcome) })
	}
}

func profilePublication(t *testing.T, outcome string) {
	t.Helper()
	s, session := credentialSessionServer(t)
	engineSQL(t, s.store, `UPDATE teams SET next_run_number=2 WHERE name='a'`)
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "https://objects.test", SigningKey: bytes.Repeat([]byte{1}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	s.WithDataObjects(objects)
	h := s.Handler()
	w := credentialRequestHTTP(t, h, session, `{"grants":["runs.read","runs.write","logs.read"]}`, false)
	var root credentialResponse
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &root) != nil {
		t.Fatalf("profile root: %d %s", w.Code, w.Body.String())
	}
	transport := intakeGitHubTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		if r.URL.Host == "objects.test" {
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Fatal("publication credential reached objects")
			}
			objects.ServeHTTP(w, r)
		} else {
			h.ServeHTTP(w, r)
		}
		if w.Code >= 400 {
			t.Logf("publication HTTP %s %s: %d %s", r.Method, r.URL.RequestURI(), w.Code, w.Body.String())
		}
		return w.Result(), nil
	})
	options := client.EngineOptions{URL: "https://controller.test", HTTP: &http.Client{Transport: transport}, Bearer: func(context.Context) (string, error) { return root.Capability, nil }}
	now := time.Now().UTC()
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Inline: true, Steps: []nodeproto.StepSpec{{ID: "run"}}}}}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	request := runspec.Request{Version: 1, Team: "local", Pipeline: "ci", Origin: runspec.OriginLocal, Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "git:example.test/acme/repo"}, Ref: "main", SHA: "local-sha"}, RequestedArgs: map[string]string{}}
	state := store.LocalPublication{RunSnapshot: store.RunSnapshot{Run: store.RunRow{ID: "local-run", Team: "local", Pipeline: "ci", State: store.RunSuccess, Request: runspec.SealedRequest{Request: request}, Spec: &runspec.Spec{Declarations: runspec.CheckoutDeclarations{Version: 1}, Resolved: runspec.Resolved{Args: map[string]string{}, SecretArgs: []string{}, SecretNames: []string{"LOCAL_TOKEN"}, Guards: runspec.Guards{Passed: true}}}, Plan: raw, FinishedAt: &now}, Nodes: []store.NodeRow{{Ref: store.NodeRef{RunID: "local-run", NodeID: "job"}, Spec: rawJob(t, plan.Jobs[0]), Status: store.StatusDone, Outcome: store.NodeSuccess, Attempt: 1, Generation: 1, StartedAt: &now, FinishedAt: &now}}}}
	state.Run.State = store.RunState(outcome)
	state.Nodes[0].Outcome = store.NodeState(outcome)
	masked, exit := "", 0
	if outcome == "failed" {
		masked, exit = "retained masked error ***", 7
	}
	terminal := store.NodeResult{Outcome: store.NodeState(outcome), Reason: "local-result", MaskedError: masked, ExitCode: &exit}
	if outcome == "failed" {
		terminal.Failure = &nodeproto.JobFailure{Job: "job", Stage: "action", Error: &apierr.Error{Code: apierr.BadRequest, Message: masked}}
	}
	state.Results = map[string]store.NodeResult{"job": terminal}
	content := map[blob.Digest][]byte{}
	put := func(body string) blob.Digest {
		digest := blob.Digest(fmt.Sprintf("%x", sha256.Sum256([]byte(body))))
		content[digest] = []byte(body)
		return digest
	}
	artifact := put("artifact bytes")
	manifest := put(fmt.Sprintf(`{"entries":[{"path":"result.txt","digest":%q,"size":14,"mode":420}]}`, artifact))
	output := put(`{"answer":42}`)
	a := runspec.AttemptIdentity{Team: "local", RunID: "local-run", NodeID: "job", Attempt: 1, Generation: 1}
	for _, part := range []struct {
		kind   blob.Kind
		digest blob.Digest
	}{{blob.KindOutput, output}, {blob.KindManifest, manifest}} {
		key := blob.ResultRecordKey(a, request.Git.Repository.Key, blob.RefScope{Ref: "main"}, "ci", part.kind)
		record := blob.RecordLookupResult{Found: true, Record: blob.Record{Key: key, Digest: part.digest}}
		if part.kind == blob.KindManifest {
			record.Members = []blob.Member{{Team: "local", ManifestDigest: manifest, Digest: artifact, Path: "result.txt", Size: 14, Mode: 420}}
		}
		state.Records = append(state.Records, record)
	}
	run, err := client.PublishLocalRun(t.Context(), options, client.PublicationInput{State: state, Logs: publicationLogReader{}, OpenBlob: func(_ context.Context, digest blob.Digest) (io.ReadCloser, int64, error) {
		body, ok := content[digest]
		if !ok {
			return nil, 0, fmt.Errorf("unselected digest %s", digest)
		}
		return io.NopCloser(bytes.NewReader(body)), int64(len(body)), nil
	}})
	if err != nil {
		t.Fatalf("publish %s: %v", run, err)
	}
	tenant, err := s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := tenant.ReadSnapshot(t.Context(), run)
	if err != nil || snapshot.Run.State != store.RunState(outcome) || snapshot.Run.Owner != nil || len(snapshot.Nodes) != 1 || snapshot.Nodes[0].OutputRecord == nil || snapshot.Nodes[0].ManifestRecord == nil {
		t.Fatalf("published history: %+v %v", snapshot, err)
	}
	exported, err := tenant.ReadLocalPublication(t.Context(), run)
	if err != nil || exported.Results["job"].MaskedError != masked || exported.Results["job"].ExitCode == nil || *exported.Results["job"].ExitCode != exit {
		t.Fatalf("publication lost local diagnostics: %+v %v", exported.Results, err)
	}
	for _, table := range []string{"triggers", "eval_units"} {
		var count int
		if err := s.store.DB().QueryRowContext(t.Context(), "SELECT count(*) FROM "+table+" WHERE team='a' AND "+map[string]string{"triggers": "id", "eval_units": "run_id"}[table]+"=?", run).Scan(&count); err != nil || count != 0 {
			t.Fatalf("publication created %s: %d %v", table, count, err)
		}
	}
	reader, err := client.NewLogReader(options)
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.ReadLogs(t.Context(), run, controllerwire.LogQuery{Job: "job", NoEvents: true})
	if err != nil || !page.Complete || len(page.Records) != 1 || !strings.Contains(page.Records[0].Line, "masked local output ***") {
		t.Fatalf("published retained logs: %+v %v", page, err)
	}
	var active int
	if err := s.store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM capabilities WHERE bind_run=? AND revoked_at IS NULL`, run).Scan(&active); err != nil || active != 0 {
		t.Fatalf("publication left live credentials: %d %v", active, err)
	}
}

func rawJob(t *testing.T, job nodeproto.JobSpec) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
