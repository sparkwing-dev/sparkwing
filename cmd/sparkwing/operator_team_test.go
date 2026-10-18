package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestOperatorTeamsCreateCLIThroughStore(t *testing.T) {
	for _, format := range []string{"plain", "json", "pretty"} {
		t.Run(format, func(t *testing.T) {
			st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := st.Close(); err != nil {
					t.Error(err)
				}
			})
			raw := "swk_" + strings.Repeat("A", 43)
			if _, err := controller.EnsureBootstrapAdminToken(t.Context(), st, raw); err != nil {
				t.Fatal(err)
			}
			h := controller.New(st, nil).Handler()
			calls, closes := 0, 0
			connect := func(_ context.Context, profile string) (controllerwire.OperatorManager, func(), error) {
				if profile != "deployment" {
					t.Fatalf("profile=%q", profile)
				}
				manager, err := client.NewOperatorManager(client.EngineOptions{URL: "https://controller.test", Bearer: func(context.Context) (string, error) { return raw, nil }, HTTP: &http.Client{Transport: logRoundTrip(func(r *http.Request) (*http.Response, error) {
					calls++
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					if r.Method == "GET" && strings.Contains(r.URL.Path, "/operator/teams/") {
						for _, field := range []string{"balance_micro", "billing", "events", "owners"} {
							if strings.Contains(w.Body.String(), field) {
								t.Fatalf("deployment team read exposed %s", field)
							}
						}
					}
					return w.Result(), nil
				})}})
				return manager, func() { closes++ }, err
			}
			args := []string{"create", "--team", "cloud-support", "--display-name", "Cloud support", "--profile", "deployment", "-o", format}
			var out bytes.Buffer
			if err := runOperatorTeamsCommandArgs(t.Context(), args, &out, connect); err != nil {
				t.Fatal(err)
			}
			credential := strings.TrimSpace(out.String())
			if format == "json" {
				var created controllerwire.OperatorTeamCreated
				if err := json.Unmarshal(out.Bytes(), &created); err != nil {
					t.Fatal(err)
				}
				if created.Team != "cloud-support" || created.AccountID == "" {
					t.Fatalf("created=%+v", created)
				}
				credential = created.Capability
			}
			if !strings.HasPrefix(credential, "swk_") || strings.ContainsAny(credential, "\n\t ") {
				t.Fatalf("credential output=%q", out.String())
			}
			r := httptest.NewRequest("GET", "https://controller.test/api/v1/team/members", nil)
			r.Header.Set("Authorization", "Bearer "+credential)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("issued admin: %d %s", w.Code, w.Body)
			}
			out.Reset()
			err = runOperatorTeamsCommandArgs(t.Context(), args, &out, connect)
			var api *apierr.Error
			if !errors.As(err, &api) || api.Code != apierr.Conflict || out.Len() != 0 || calls != 2 || closes != 2 {
				t.Fatalf("duplicate: %v output=%q calls=%d closes=%d", err, out.String(), calls, closes)
			}
			out.Reset()
			if err = runOperatorTeamsCommandArgs(t.Context(), []string{"show", "--team", "cloud-support", "--profile", "deployment", "-o", format}, &out, connect); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), launcher.TeamNodeLabel) || !strings.Contains(out.String(), launcher.TeamNodeValue("cloud-support")) {
				t.Fatalf("show omits node assignment: %s", out.String())
			}
			if calls != 3 || closes != 3 {
				t.Fatalf("show did not use operator authority: calls=%d closes=%d", calls, closes)
			}
		})
	}
}

func TestOperatorTeamsCreateCLIValidatesBeforeConnecting(t *testing.T) {
	for _, args := range [][]string{{"create"}, {"create", "--team", "INVALID"}, {"create", "--team", "ok-team", "--scope", "operator.tokens"}} {
		var out bytes.Buffer
		connect := func(context.Context, string) (controllerwire.OperatorManager, func(), error) {
			t.Fatal("invalid command connected")
			return nil, nil, io.EOF
		}
		if err := runOperatorTeamsCommandArgs(t.Context(), args, &out, connect); err == nil || exitCodeFor(err) != 2 || out.Len() != 0 {
			t.Fatalf("args=%v err=%v output=%q", args, err, out.String())
		}
	}
}
