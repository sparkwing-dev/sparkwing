package client

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestNativeAnnotationPreservesLocationAndStep(t *testing.T) {
	for _, step := range []string{"", "compile"} {
		t.Run(step, func(t *testing.T) {
			input := nodewire.AnnotationRequest{Step: step, Annotation: nodewire.Annotation{Level: "warn", Message: "masked note", File: "source.go", Line: 7, EndLine: 9, Column: 3}}
			transport := testEngine(t, func(request *http.Request) (*http.Response, error) {
				var body controllerwire.AnnotationWrite
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				want := controllerwire.AnnotationWrite{Annotation: nodewire.Annotation{Level: input.Level, Message: input.Message, File: input.File, Line: input.Line, EndLine: input.EndLine, Column: input.Column}, StepID: step}
				if !reflect.DeepEqual(body, want) {
					t.Fatalf("annotation changed in transit: %+v, want %+v", body, want)
				}
				suffix := "/annotations"
				if step != "" {
					suffix = "/steps" + suffix
				}
				if request.Method != http.MethodPost || request.URL.Path != "/api/v1/runs/run/jobs/job"+suffix {
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
		if err := transport.AppendAnnotation(t.Context(), "run", "job", controllerwire.AnnotationWrite{Annotation: nodewire.Annotation{Level: "info", Message: "note"}}); err == nil {
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
	if err := process.Annotation(t.Context(), nodewire.AnnotationRequest{Annotation: nodewire.Annotation{Level: "info"}}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeChildPreservesOmittedRepositoryIntent(t *testing.T) {
	for _, repo := range []string{"", "git:example.test/team/repo"} {
		t.Run(repo, func(t *testing.T) {
			transport := testEngine(t, func(request *http.Request) (*http.Response, error) {
				var body struct {
					Request struct {
						Source           runspec.Source             `json:"source"`
						SecretArgs       runspec.Optional[[]string] `json:"secret_args"`
						LocalSecretNames []string                   `json:"local_secret_names"`
						Lineage          struct {
							RepoInherited bool `json:"repo_inherited"`
						} `json:"lineage"`
					} `json:"request"`
				}
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Request.Source.Entrypoint != "" || body.Request.SecretArgs.Set || len(body.Request.LocalSecretNames) != 0 {
					t.Fatal("child inherited parent pipeline declaration")
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
			process := &NativeProcessClient{transport: transport, run: "run", job: "job", request: runspec.Request{LocalSecretNames: []string{"TOKEN"}, Source: runspec.Source{Entrypoint: "Build"}, SecretArgs: runspec.Optional[[]string]{Set: true, Value: []string{"password"}}, Git: runspec.Git{Repository: runspec.Repository{Key: "git:example.test/team/repo"}}}}
			if _, err := process.StartChild(t.Context(), "deploy", nil, repo, "", false); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeChildrenUseTheirOwnJobAndStepSelections(t *testing.T) {
	selected := func(value string) runspec.Setting[string] {
		return runspec.Setting[string]{Optional: runspec.Optional[string]{Set: true, Value: value}}
	}
	for _, tc := range []struct {
		name    string
		options runspec.Options
	}{
		{"job", runspec.Options{Only: selected("dynamic-child")}},
		{"step-window", runspec.Options{StartAt: selected("compile"), StopAt: selected("package")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := tc.options
			options.NoMemo = runspec.Setting[bool]{Optional: runspec.Optional[bool]{Set: true, Value: true}}
			transport := testEngine(t, func(request *http.Request) (*http.Response, error) {
				var body struct {
					Request runspec.Request `json:"request"`
				}
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				got := body.Request.Options
				if got.Only.Set || got.StartAt.Set || got.StopAt.Set {
					t.Errorf("child inherited parent selectors: only=%+v, start=%+v, stop=%+v", got.Only, got.StartAt, got.StopAt)
				}
				if !got.NoMemo.Set || !got.NoMemo.Value {
					t.Error("child lost the inherited memo preference")
				}
				response, err := json.Marshal(controllerwire.CreateRunResponse{RunID: "child", Number: 2})
				return engineResponse(http.StatusCreated, string(response)), err
			})
			process := &NativeProcessClient{transport: transport, run: "parent", job: "dynamic-child", request: runspec.Request{Options: options}}
			if _, err := process.StartChild(t.Context(), "weather-report", nil, "", "", false); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(process.request.Options, options) {
				t.Fatal("child creation changed parent selections")
			}
		})
	}
}

func FuzzNativeChildrenResolveTheirOwnLocalSecretNames(f *testing.F) {
	f.Add("TOKEN PASSWORD", "", "")
	f.Add("TOKEN", "git:example.test/team/other", "main")
	f.Add("", "", "feature")
	f.Fuzz(func(t *testing.T, names, repo, branch string) {
		transport := testEngine(t, func(request *http.Request) (*http.Response, error) {
			var body struct {
				Request      runspec.Request `json:"request"`
				LocalSecrets []string        `json:"local_secrets"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body.Request.LocalSecretNames) != 0 || len(body.LocalSecrets) != 0 {
				t.Fatal("child inherited parent local-secret requirements")
			}
			response, err := json.Marshal(controllerwire.CreateRunResponse{RunID: "child", Number: 2})
			return engineResponse(http.StatusCreated, string(response)), err
		})
		process := &NativeProcessClient{transport: transport, run: "parent", job: "build", request: runspec.Request{LocalSecretNames: strings.Fields(names), Git: runspec.Git{Repository: runspec.Repository{Key: runspec.RepoKey(repo)}}}}
		if _, err := process.StartChild(t.Context(), "Deploy", nil, repo, branch, false); err != nil {
			t.Fatal(err)
		}
	})
}

func TestProvenLatestRefusesAControllerThatIgnoredTheFilter(t *testing.T) {
	for _, marked := range []bool{false, true} {
		transport := testEngine(t, func(request *http.Request) (*http.Response, error) {
			if strings.HasSuffix(request.URL.Path, "/output") {
				return engineResponse(http.StatusOK, `{"output":{"tree":"t"}}`), nil
			}
			if request.URL.Query().Get("from_proven_run") != "true" {
				t.Fatalf("proven filter not requested: %s", request.URL)
			}
			body, err := json.Marshal(controllerwire.LatestRun{RunSummary: runstate.RunSummary{ID: "source"}, ProvenAtCommit: marked})
			if err != nil {
				t.Fatal(err)
			}
			return engineResponse(http.StatusOK, string(body)), nil
		})
		process := &NativeProcessClient{transport: transport, run: "run", job: "job"}
		run, _, err := process.Latest(t.Context(), "gate", "record-tree", 0, false, true)
		if marked && (err != nil || run != "source") {
			t.Fatalf("marked answer refused: %q %v", run, err)
		}
		if !marked && err == nil {
			t.Fatal("an answer from a controller that ignored the proven filter was accepted")
		}
	}
}

func TestNativeDetachedChildRetainsParentSourceAndLineage(t *testing.T) {
	parent := runspec.Request{Version: 1, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, Ref: "refs/heads/main", SHA: strings.Repeat("a", 40)}, Source: runspec.Source{Ref: strings.Repeat("a", 40), Location: runspec.SourceLocation{RemoteURL: "https://github.com/acme/repo", PipelineDir: ".sparkwing"}}}
	transport := testEngine(t, func(request *http.Request) (*http.Response, error) {
		var body controllerwire.CreateRunRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		got := body.Request
		if !got.Lineage.Detached || got.Lineage.ParentRunID != "parent" || got.Lineage.ParentJobID != "deploy" || !reflect.DeepEqual(got.Git, parent.Git) || !reflect.DeepEqual(got.Source, parent.Source) {
			t.Fatalf("child lost parent identity: %+v", got)
		}
		response, err := json.Marshal(controllerwire.CreateRunResponse{RunID: "scan", Number: 2})
		return engineResponse(http.StatusCreated, string(response)), err
	})
	process := &NativeProcessClient{transport: transport, run: "parent", job: "deploy", request: parent}
	if run, err := process.StartChild(t.Context(), "deploy-scan", map[string]string{"environment": "staging"}, "", "", true); err != nil || run != "scan" {
		t.Fatalf("start = %q, %v", run, err)
	}
}
