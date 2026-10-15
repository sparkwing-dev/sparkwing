package acceptance_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestSurfaceRunnerEnrollmentCredentialTransport(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		t.Run(endpoint, func(t *testing.T) {
			metadata := controllerwire.TokenMetadata{Prefix: "swk_00000001", Kind: "runner", Principal: "runner:desktop", Scopes: []capability.Grant{capability.NodesClaim, capability.TriggersClaim, capability.RunsState}, ReservedPools: []string{"prod"}, Metered: false}
			calls := 0
			manager, err := client.NewTokenManager(client.EngineOptions{URL: endpoint, Bearer: func(context.Context) (string, error) { return "member-credential", nil }, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Header.Get("Authorization") != "Bearer member-credential" {
					t.Fatal("credential missing")
				}
				var response any
				switch calls {
				case 1:
					if req.Method != "POST" || req.URL.Path != "/api/v1/tokens" || req.Header.Get("Idempotency-Key") != "enroll-once" {
						t.Fatalf("enrollment: %s %s", req.Method, req.URL)
					}
					var body controllerwire.TokenCreate
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if body.Kind != "runner" || body.Principal != metadata.Principal || !reflect.DeepEqual(body.Scopes, metadata.Scopes) || body.TTLSecs != nil {
						t.Fatalf("enrollment proposal: %+v", body)
					}
					response = controllerwire.TokenCreated{Token: "swk_00000001_secret", Metadata: metadata}
				case 2:
					if req.Method != "GET" || req.URL.Path != "/api/v1/tokens/"+metadata.Prefix {
						t.Fatalf("identity check: %s %s", req.Method, req.URL)
					}
					response = metadata
				case 3:
					if req.Method != "DELETE" || req.URL.Path != "/api/v1/tokens/"+metadata.Prefix {
						t.Fatalf("revocation: %s %s", req.Method, req.URL)
					}
					response = controllerwire.TokenRevoked{Prefix: metadata.Prefix, Revoked: true}
				default:
					t.Fatal("unexpected request")
				}
				body, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}}, false)
			if err != nil {
				t.Fatal(err)
			}
			created, err := manager.CreateToken(t.Context(), controllerwire.TokenCreate{Kind: "runner", Principal: metadata.Principal, Scopes: metadata.Scopes, IdempotencyKey: "enroll-once"})
			if err != nil || created.Token != "swk_00000001_secret" || !reflect.DeepEqual(created.Metadata, metadata) {
				t.Fatalf("created: %+v %v", created, err)
			}
			read, err := manager.ReadToken(t.Context(), metadata.Prefix)
			if err != nil || !reflect.DeepEqual(read, metadata) {
				t.Fatalf("read: %+v %v", read, err)
			}
			revoked, err := manager.RevokeToken(t.Context(), metadata.Prefix)
			if err != nil || !revoked.Revoked || calls != 3 {
				t.Fatalf("revoked: %+v %v; calls=%d", revoked, err, calls)
			}
		})
	}
}
