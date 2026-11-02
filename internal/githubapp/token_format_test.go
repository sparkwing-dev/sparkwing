package githubapp_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
)

func TestInstallationTokenFormatOverrideAndAuthorization(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, optIn := range []bool{false, true} {
		for _, size := range []int{40, 520, 4096} {
			t.Run(fmt.Sprintf("opt-in=%t/bytes=%d", optIn, size), func(t *testing.T) {
				token := "ghs_" + strings.Repeat("aB1_-.", (size-4)/6) + strings.Repeat("z", (size-4)%6)
				if size == 40 {
					token = "ghs_" + strings.Repeat("aB1", 12)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.Method + " " + r.URL.Path {
					case "POST /app/installations/7/access_tokens":
						want := ""
						if optIn {
							want = "enabled"
						}
						if r.Header.Get("X-GitHub-Stateless-S2S-Token") != want {
							t.Error("token format override differs from configuration")
						}
						w.WriteHeader(http.StatusCreated)
						if err := json.NewEncoder(w).Encode(githubapp.Token{Token: token}); err != nil {
							t.Error(err)
						}
					case "GET /repositories/701":
						if r.Header.Get("X-GitHub-Stateless-S2S-Token") != "" || r.Header.Get("Authorization") != "token "+token {
							t.Error("repository request changed authorization or carried the mint-only override")
						}
						if err := json.NewEncoder(w).Encode(githubapp.Repository{ID: 701}); err != nil {
							t.Error(err)
						}
					default:
						t.Error("unexpected GitHub request")
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()
				c := githubapp.New(githubapp.Config{AppID: 1, PrivateKey: key, APIURL: server.URL, HTTP: server.Client(), StatelessInstallationTokens: optIn})
				got, err := c.InstallationTokenByIDs(t.Context(), 7, []int64{701}, map[string]string{"contents": "read"})
				if err != nil || got.Token != token {
					t.Fatalf("minted token did not round trip: %v", err)
				}
				if _, err := c.RepositoryByID(t.Context(), got.Token, 701); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
