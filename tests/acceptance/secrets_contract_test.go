package acceptance_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestSurfaceSecretsTransport(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		t.Run(endpoint, func(t *testing.T) {
			calls := 0
			metadata := controllerwire.SecretMetadata{Name: "DEPLOY_KEY", Pipeline: "deploy", Principal: "alice", Masked: true, Bound: true, OnlyOn: "prod", CreatedAt: 1, UpdatedAt: 2}
			manager, err := client.NewSecretManager(client.EngineOptions{URL: endpoint, Bearer: func(ctx context.Context) (string, error) { return "member-credential", nil }, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Header.Get("Authorization") != "Bearer member-credential" {
					t.Fatal("missing credential")
				}
				var result any
				switch calls {
				case 1:
					if req.Method != "POST" || req.URL.Path != "/api/v1/secrets" || req.Header.Get("Idempotency-Key") != "write-key" {
						t.Fatalf("write: %s %s", req.Method, req.URL)
					}
					var input controllerwire.SecretWrite
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatal(err)
					}
					if input.Name != "DEPLOY_KEY" || input.Value != "private" || input.Pipeline != "deploy" || input.OnlyOn != "prod" || !input.Masked || input.Shared {
						t.Fatalf("write lost scope: %+v", input)
					}
					result = controllerwire.SecretWritten{Secret: metadata, Replayed: true}
				case 2:
					if req.Method != "GET" || req.URL.Path != "/api/v1/secrets" {
						t.Fatalf("list: %s %s", req.Method, req.URL)
					}
					result = controllerwire.Secrets{Secrets: []controllerwire.SecretMetadata{metadata}}
				case 3:
					if req.Method != "GET" || req.URL.Path != "/api/v1/secrets/DEPLOY_KEY" || req.URL.Query().Get("pipeline") != "deploy" {
						t.Fatalf("read: %s %s", req.Method, req.URL)
					}
					result = controllerwire.SecretValue{SecretMetadata: metadata, Value: "private"}
				case 4:
					if req.Method != "DELETE" || req.URL.Path != "/api/v1/secrets/DEPLOY_KEY" || req.URL.Query().Get("pipeline") != "deploy" {
						t.Fatalf("delete: %s %s", req.Method, req.URL)
					}
					result = controllerwire.SecretDeletion{Name: "DEPLOY_KEY", Pipeline: "deploy", Deleted: false}
				case 5:
					if req.Method != "POST" || req.URL.Path != "/api/v1/secrets/rotate" || req.Header.Get("Idempotency-Key") != "rotate-key" {
						t.Fatalf("rotate: %s %s", req.Method, req.URL)
					}
					result = controllerwire.SecretRotation{Rotated: 3, Skipped: []controllerwire.SecretSelector{{Name: "OTHER", Pipeline: "release"}}, Replayed: true}
				default:
					t.Fatal("unexpected request")
				}
				data, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			written, err := manager.WriteSecret(t.Context(), controllerwire.SecretWrite{Name: "DEPLOY_KEY", Value: "private", Pipeline: "deploy", Masked: true, OnlyOn: "prod", IdempotencyKey: "write-key"})
			if err != nil || written.Secret != metadata || !written.Replayed {
				t.Fatalf("written: %+v %v", written, err)
			}
			listed, err := manager.ListSecrets(t.Context())
			if err != nil || len(listed.Secrets) != 1 || listed.Secrets[0] != metadata {
				t.Fatalf("list: %+v %v", listed, err)
			}
			selector := controllerwire.SecretSelector{Name: "DEPLOY_KEY", Pipeline: "deploy"}
			value, err := manager.ReadSecret(t.Context(), selector)
			if err != nil || value.Value != "private" || value.SecretMetadata != metadata {
				t.Fatalf("read: %+v %v", value, err)
			}
			deleted, err := manager.DeleteSecret(t.Context(), selector)
			if err != nil || deleted.Deleted {
				t.Fatalf("delete: %+v %v", deleted, err)
			}
			rotated, err := manager.RotateSecrets(t.Context(), controllerwire.SecretRotationRequest{IdempotencyKey: "rotate-key"})
			if err != nil || rotated.Rotated != 3 || len(rotated.Skipped) != 1 || !rotated.Replayed || calls != 5 {
				t.Fatalf("rotation: %+v %v", rotated, err)
			}
		})
	}
}
