package client_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
)

type logSubjectTransport func(*http.Request) (*http.Response, error)

func (f logSubjectTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLogUploaderSealsOnlyItsGrantedSubject(t *testing.T) {
	var sealed map[string]json.RawMessage
	transport := logSubjectTransport(func(r *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, `{"attempt":2,"generation":7,"prefix":"logs/r90/teams/acme/runs/run-1/jobs/build/a2-g7/"}`
		if strings.HasSuffix(r.URL.Path, "/log-manifest") {
			if err := json.NewDecoder(r.Body).Decode(&sealed); err != nil {
				t.Fatal(err)
			}
			status, body = http.StatusNoContent, ""
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	u := client.NewWithToken("https://controller.test", &http.Client{Transport: transport}, "credential").NewLogSegmentUploader(t.Context(), "run-1", "build")
	if err := u.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	subject := u.Subject()
	if subject.Kind != blob.LogJobAttempt || subject.Team != "acme" || subject.RunID != "run-1" || subject.Attempt.NodeID != "build" || subject.Attempt.Attempt != 2 || subject.Attempt.Generation != 7 {
		t.Fatalf("subject = %+v", subject)
	}
	subject.Attempt.NodeID = "other"
	if err := u.PutManifest(t.Context(), logseg.Manifest{Version: logseg.ManifestVersion, Subject: &subject, Status: logseg.StatusComplete}); !errors.Is(err, blob.ErrIntegrity) || sealed != nil {
		t.Fatalf("foreign subject sealed: %v, %s", err, sealed)
	}
	want := u.Subject()
	if want.Attempt.NodeID != "build" {
		t.Fatal("caller changed uploader's identity")
	}
	if err := u.PutManifest(t.Context(), logseg.Manifest{Version: logseg.ManifestVersion, Subject: &want, Status: logseg.StatusComplete}); err != nil {
		t.Fatal(err)
	}
	var got blob.LogSubject
	if err := json.Unmarshal(sealed["subject"], &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("sealed subject = %+v, %v", got, err)
	}
	for _, key := range []string{"run", "node", "attempt", "generation"} {
		if _, ok := sealed[key]; ok {
			t.Errorf("sealed retired identity field %q", key)
		}
	}
}

func TestLogUploaderRejectsChangedGrantTeam(t *testing.T) {
	team := "acme"
	transport := logSubjectTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"attempt":2,"generation":7,"prefix":"logs/r90/teams/` + team + `/runs/run-1/jobs/build/a2-g7/"}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	u := client.NewWithToken("https://controller.test", &http.Client{Transport: transport}, "credential").NewLogSegmentUploader(t.Context(), "run-1", "build")
	if err := u.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	team = "other"
	if err := u.Open(t.Context()); !errors.Is(err, blob.ErrIntegrity) || u.Subject().Team != "acme" {
		t.Fatalf("grant changed team: %+v, %v", u.Subject(), err)
	}
}
