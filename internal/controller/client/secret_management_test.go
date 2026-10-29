package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestSecretManagementMetadataDoesNotExposeValues(t *testing.T) {
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		return engineResponse(200, `{"secrets":[{"name":"TOKEN","value":"must-not-escape","principal":"alice","masked":true,"shared":true,"bound":true,"created_at":1,"updated_at":2,"only_on":"prod"}]}`), nil
	})
	result, err := c.ListSecrets(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result)
	if err != nil || strings.Contains(string(data), "must-not-escape") || strings.Contains(string(data), `"value"`) || len(result.Secrets) != 1 || result.Secrets[0].OnlyOn != "prod" {
		t.Fatalf("metadata: %s %v", data, err)
	}
}

func TestSecretManagementReadsScopedAndEmptyValues(t *testing.T) {
	for _, pipeline := range []string{"", "deploy & release"} {
		repo := ""
		if pipeline != "" {
			repo = "acme/web"
		}
		c := testEngine(t, func(req *http.Request) (*http.Response, error) {
			if req.URL.EscapedPath() != "/api/v1/secrets/deploy%2Fkey" || req.URL.Query().Get("repo") != repo || req.URL.Query().Get("pipeline") != pipeline {
				t.Fatalf("selector lost: %s", req.URL)
			}
			data, err := json.Marshal(controllerwire.SecretValue{SecretMetadata: controllerwire.SecretMetadata{Name: "deploy/key", Shared: true}, Value: ""})
			if err != nil {
				t.Fatal(err)
			}
			return engineResponse(200, string(data)), nil
		})
		result, err := c.ReadSecret(t.Context(), controllerwire.SecretSelector{Name: "deploy/key", Repo: repo, Pipeline: pipeline})
		if err != nil || result.Value != "" || result.Name != "deploy/key" {
			t.Fatalf("empty or shared fallback value: %+v %v", result, err)
		}
	}
}

func TestSecretManagementRetriesExactWriteAndRotation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, rotation := range []bool{false, true} {
			var bodies, keys []string
			c := testEngine(t, func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
				bodies = append(bodies, string(body))
				keys = append(keys, req.Header.Get("Idempotency-Key"))
				if len(bodies) == 1 {
					return nil, io.EOF
				}
				if rotation {
					return engineResponse(200, `{"rotated":1,"skipped":[],"git_credentials":0,"replayed":true}`), nil
				}
				return engineResponse(200, `{"secret":{"name":"REGION","principal":"alice","masked":false,"shared":false,"bound":true,"created_at":1,"updated_at":1},"replayed":true}`), nil
			})
			var err error
			if rotation {
				_, err = c.RotateSecrets(t.Context(), controllerwire.SecretRotationRequest{IdempotencyKey: "stable"})
			} else {
				_, err = c.WriteSecret(t.Context(), controllerwire.SecretWrite{Name: "REGION", Value: "", IdempotencyKey: "stable"})
			}
			if err != nil || len(bodies) != 2 || bodies[0] != bodies[1] || !reflect.DeepEqual(keys, []string{"stable", "stable"}) {
				t.Fatalf("retry changed: %v %v %v", bodies, keys, err)
			}
			if rotation && bodies[0] != "" {
				t.Fatalf("rotation sent a body the controller refuses: %q", bodies[0])
			}
			if !rotation && (!strings.Contains(bodies[0], `"value":""`) || !strings.Contains(bodies[0], `"masked":false`)) {
				t.Fatalf("explicit zero lost: %s", bodies[0])
			}
		}
	})
}

func TestSecretManagementRejectsMissingOrConflictingResponses(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"secrets":[{}]}`,
		`{"secrets":[{"name":"TOKEN","principal":"a","masked":true,"shared":false,"bound":true,"created_at":1,"updated_at":1},{"name":"TOKEN","principal":"b","masked":true,"shared":false,"bound":true,"created_at":1,"updated_at":1}]}`,
		`{"secrets":[{"name":"TOKEN","principal":"a","pipeline":"deploy","masked":true,"shared":true,"bound":true,"created_at":1,"updated_at":1}]}`,
	} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, body), nil })
		if _, err := c.ListSecrets(t.Context()); err == nil {
			t.Fatalf("invalid metadata accepted: %s", body)
		}
	}
	for _, body := range []string{
		`{"name":"TOKEN","principal":"a","masked":true,"shared":false,"bound":true,"created_at":1,"updated_at":1}`,
		`{"name":"TOKEN","principal":"a","masked":true,"shared":false,"bound":true,"created_at":1,"updated_at":1,"value":null}`,
		`{"name":"OTHER","principal":"a","masked":true,"shared":false,"bound":true,"created_at":1,"updated_at":1,"value":"secret"}`,
	} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, body), nil })
		if _, err := c.ReadSecret(t.Context(), controllerwire.SecretSelector{Name: "TOKEN"}); err == nil {
			t.Fatalf("invalid value read accepted: %s", body)
		}
	}
	for _, body := range []string{
		`{"secret":{"name":"TOKEN","principal":"a","masked":true,"shared":false,"bound":true,"created_at":1,"updated_at":1},"replayed":false}`,
		`{"secret":{"name":"TOKEN","principal":"a","masked":true,"shared":false,"bound":false,"only_on":"prod","created_at":1,"updated_at":1},"replayed":false}`,
	} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, body), nil })
		if _, err := c.WriteSecret(t.Context(), controllerwire.SecretWrite{Name: "TOKEN", Masked: true, OnlyOn: "prod", IdempotencyKey: "key"}); err == nil {
			t.Fatalf("restriction or encryption lost: %s", body)
		}
	}
}

func TestSecretManagementRejectsInvalidRotationAndDeletion(t *testing.T) {
	for _, body := range []string{`{}`, `{"rotated":-1,"skipped":[],"git_credentials":0,"replayed":false}`, `{"rotated":1,"skipped":[{}],"git_credentials":0,"replayed":false}`, `{"rotated":1,"skipped":[{"name":"TOKEN"},{"name":"TOKEN"}],"git_credentials":0,"replayed":false}`} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, body), nil })
		if _, err := c.RotateSecrets(t.Context(), controllerwire.SecretRotationRequest{IdempotencyKey: "key"}); err == nil {
			t.Fatalf("invalid rotation accepted: %s", body)
		}
	}
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		return engineResponse(200, `{"name":"TOKEN","pipeline":"other","deleted":true}`), nil
	})
	if _, err := c.DeleteSecret(t.Context(), controllerwire.SecretSelector{Name: "TOKEN", Repo: "acme/web", Pipeline: "deploy"}); err == nil {
		t.Fatal("deletion answered with a body accepted")
	}
}

func TestSecretManagementValidatesBeforeTransport(t *testing.T) {
	c := testEngine(t, func(*http.Request) (*http.Response, error) { t.Fatal("invalid input reached HTTP"); return nil, io.EOF })
	for _, input := range []controllerwire.SecretWrite{{}, {Name: "TOKEN"}, {Name: "../TOKEN", IdempotencyKey: "key"}, {Name: "TOKEN", Repo: "acme/web", Pipeline: "deploy", Shared: true, IdempotencyKey: "key"}, {Name: "TOKEN", Pipeline: "deploy", IdempotencyKey: "key"}, {Name: "TOKEN", Repo: "acme/web", IdempotencyKey: "key"}, {Name: "TOKEN", OnlyOn: "\r\n", IdempotencyKey: "key"}} {
		if _, err := c.WriteSecret(t.Context(), input); err == nil {
			t.Fatalf("invalid write accepted: %+v", input)
		}
	}
	for _, selector := range []controllerwire.SecretSelector{{}, {Name: "../TOKEN"}, {Name: "TOKEN", Repo: "acme/web", Pipeline: "\n"}, {Name: "TOKEN", Pipeline: "deploy"}, {Name: "TOKEN", Repo: "acme/web"}} {
		if _, err := c.ReadSecret(t.Context(), selector); err == nil {
			t.Fatalf("invalid read accepted: %+v", selector)
		}
		if _, err := c.DeleteSecret(t.Context(), selector); err == nil {
			t.Fatalf("invalid delete accepted: %+v", selector)
		}
	}
	if _, err := c.RotateSecrets(t.Context(), controllerwire.SecretRotationRequest{}); err == nil {
		t.Fatal("unkeyed rotation accepted")
	}
}

func TestSecretManagementPreservesStructuredRefusal(t *testing.T) {
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		return engineResponse(403, `{"error":{"code":"forbidden","message":"restricted pool required","hint":"request pool=prod","details":{"pool":"prod"}}}`), nil
	})
	want := &apierr.Error{Code: apierr.Forbidden, Message: "restricted pool required", Hint: "request pool=prod", Details: map[string]any{"pool": "prod"}}
	_, err := c.ReadSecret(context.Background(), controllerwire.SecretSelector{Name: "TOKEN"})
	var failure *apierr.Error
	if !errors.As(err, &failure) || !reflect.DeepEqual(failure, want) {
		t.Fatalf("refusal changed: %v", err)
	}
}

// The controller answers a deletion with 204, or 404 when the row is absent;
// the CLI reports both instead of failing on the empty body.
func TestSecretManagementDeletionReadsTheControllerStatus(t *testing.T) {
	for _, tc := range []struct {
		status  int
		body    string
		deleted bool
	}{{204, "", true}, {404, `{"error":{"code":"not_found","message":"secret not found"}}`, false}} {
		c := testEngine(t, func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodDelete || req.URL.Query().Get("repo") != "gh:7" || req.URL.Query().Get("pipeline") != "deploy" {
				t.Fatalf("selector lost: %s %s", req.Method, req.URL)
			}
			return engineResponse(tc.status, tc.body), nil
		})
		got, err := c.DeleteSecret(t.Context(), controllerwire.SecretSelector{Name: "TOKEN", Repo: "gh:7", Pipeline: "deploy"})
		if err != nil || got != (controllerwire.SecretDeletion{Name: "TOKEN", Repo: "gh:7", Pipeline: "deploy", Deleted: tc.deleted}) {
			t.Fatalf("status %d: %+v %v", tc.status, got, err)
		}
	}
}
