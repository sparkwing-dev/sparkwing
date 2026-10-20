package acceptance_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestSurfaceRunnerEnrollmentCredentialTransport(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		t.Run(endpoint, func(t *testing.T) {
			grants := []string{string(capability.NodesClaim), string(capability.TriggersClaim), string(capability.RunsState)}
			metadata := capability.Metadata{Prefix: "swk_00000001", Team: "team", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "desktop"}, Audience: capability.Controller, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim, capability.RunsState}, IssuedAt: time.Unix(100, 0).UTC()}
			enrollment := controllerwire.EnrollRunnerRequest{Name: "desktop", Repos: []string{"github.com/acme/*"}, ReservedPools: []string{"prod"}}
			calls := 0
			transport := &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Header.Get("Authorization") != "Bearer member-credential" {
					t.Fatal("credential missing")
				}
				var response any
				status := http.StatusOK
				switch calls {
				case 1:
					if req.Method != "POST" || req.URL.Path != "/api/v1/runners" {
						t.Fatalf("enrollment: %s %s", req.Method, req.URL)
					}
					var body controllerwire.EnrollRunnerRequest
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(body, enrollment) {
						t.Fatalf("enrollment proposal: %+v", body)
					}
					response = controllerwire.CreatedToken{Capability: "swk_00000001_secret", Prefix: metadata.Prefix, Team: metadata.Team, Grants: grants}
					status = http.StatusCreated
				case 2:
					if req.Method != "GET" || req.URL.Path != "/api/v1/tokens/"+metadata.Prefix {
						t.Fatalf("identity check: %s %s", req.Method, req.URL)
					}
					response = metadata
				case 3:
					if req.Method != "DELETE" || req.URL.Path != "/api/v1/tokens/"+metadata.Prefix {
						t.Fatalf("revocation: %s %s", req.Method, req.URL)
					}
					return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader(""))}, nil
				default:
					t.Fatal("unexpected request")
				}
				body, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}
			manager, err := client.NewRunnerTokenManager(client.EngineOptions{URL: endpoint, Bearer: func(context.Context) (string, error) { return "member-credential", nil }, HTTP: transport})
			if err != nil {
				t.Fatal(err)
			}
			created, err := manager.EnrollRunner(t.Context(), enrollment)
			if err != nil || created.Capability != "swk_00000001_secret" || created.Prefix != metadata.Prefix || !reflect.DeepEqual(created.Grants, grants) {
				t.Fatalf("created: %+v %v", created, err)
			}
			read, err := manager.ReadToken(t.Context(), metadata.Prefix)
			if err != nil || read.Kind != "runner" || read.Principal != metadata.Subject.ID || (read.IssuedAt == nil || *read.IssuedAt != metadata.IssuedAt.Unix()) || len(read.Scopes) != len(grants) {
				t.Fatalf("read: %+v %v", read, err)
			}
			for i, grant := range read.Scopes {
				if string(grant) != grants[i] {
					t.Fatalf("read grants: %+v", read.Scopes)
				}
			}
			revoked, err := manager.RevokeToken(t.Context(), metadata.Prefix)
			if err != nil || !revoked.Revoked || calls != 3 {
				t.Fatalf("revoked: %+v %v; calls=%d", revoked, err, calls)
			}
		})
	}
}
