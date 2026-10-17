package client

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestNativeAnnotationPreservesLocationAndStep(t *testing.T) {
	for _, step := range []string{"", "compile"} {
		t.Run(step, func(t *testing.T) {
			input := nodeproto.AnnotationRequest{Level: "warning", Message: "masked note", File: "source.go", Line: 7, EndLine: 9, Column: 3, Step: step}
			transport := testEngine(t, func(request *http.Request) (*http.Response, error) {
				var body controllerwire.AnnotationWrite
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				want := controllerwire.AnnotationWrite{Annotation: controllerwire.Annotation{Level: input.Level, Message: input.Message, File: input.File, Line: input.Line, EndLine: input.EndLine, Column: input.Column}, StepID: step}
				if !reflect.DeepEqual(body, want) {
					t.Fatalf("annotation changed in transit: %+v, want %+v", body, want)
				}
				suffix := "/annotations"
				if step != "" {
					suffix = "/steps" + suffix
				}
				if request.Method != "POST" || request.URL.Path != "/api/v1/runs/run/jobs/job"+suffix {
					t.Fatalf("annotation route: %s %s", request.Method, request.URL.Path)
				}
				return engineResponse(http.StatusNoContent, ""), nil
			})
			process := &NativeProcessClient{transport: transport, run: "run", job: "job"}
			if err := process.Annotation(t.Context(), input); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAnnotationRequiresEmptyNoContentAcknowledgement(t *testing.T) {
	for _, response := range []struct {
		status int
		body   string
	}{
		{200, `{}`},
		{200, `{"replayed":false,"cancel_requested":false,"state_seq":2,"generation":1,"attempt":1}`},
		{204, `{}`},
		{202, ""},
	} {
		transport := testEngine(t, func(*http.Request) (*http.Response, error) {
			return engineResponse(response.status, response.body), nil
		})
		if err := transport.AppendAnnotation(t.Context(), "run", "job", controllerwire.AnnotationWrite{Annotation: controllerwire.Annotation{Level: "info", Message: "note"}}); err == nil {
			t.Fatalf("accepted HTTP %d %s", response.status, response.body)
		}
	}
}

func TestNativeAnnotationAllowsEmptyMaskedText(t *testing.T) {
	transport := testEngine(t, func(request *http.Request) (*http.Response, error) {
		var body controllerwire.AnnotationWrite
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Message != "" {
			t.Fatalf("empty masked annotation: %+v %v", body, err)
		}
		return engineResponse(http.StatusNoContent, ""), nil
	})
	process := &NativeProcessClient{transport: transport, run: "run", job: "job"}
	if err := process.Annotation(t.Context(), nodeproto.AnnotationRequest{Level: "info"}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeChildPreservesOmittedRepositoryIntent(t *testing.T) {
	for _, repo := range []string{"", "git:example.test/team/repo"} {
		t.Run(repo, func(t *testing.T) {
			transport := testEngine(t, func(request *http.Request) (*http.Response, error) {
				var body struct {
					Request struct {
						Lineage struct {
							RepoInherited bool `json:"repo_inherited"`
						} `json:"lineage"`
					} `json:"request"`
				}
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Request.Lineage.RepoInherited != (repo == "") {
					t.Fatalf("repository omission lost: %+v", body)
				}
				response, err := json.Marshal(controllerwire.CreateRunResponse{RunID: "child", Number: 2})
				if err != nil {
					t.Fatal(err)
				}
				return engineResponse(http.StatusCreated, string(response)), nil
			})
			process := &NativeProcessClient{transport: transport, run: "run", job: "job", request: runspec.Request{Git: runspec.Git{Repository: runspec.Repository{Key: "git:example.test/team/repo"}}}}
			if _, err := process.StartChild(t.Context(), "deploy", nil, repo, ""); err != nil {
				t.Fatal(err)
			}
		})
	}
}
