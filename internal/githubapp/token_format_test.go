package githubapp_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
)

func TestInstallationTokenFormatAndAuthorization(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{40, 520, 4096} {
		t.Run(fmt.Sprintf("bytes=%d", size), func(t *testing.T) {
			token := "ghs_" + strings.Repeat("aB1_-.", (size-4)/6) + strings.Repeat("z", (size-4)%6)
			if size == 40 {
				token = "ghs_" + strings.Repeat("aB1", 12)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "POST /app/installations/7/access_tokens":
					w.WriteHeader(http.StatusCreated)
					if err := json.NewEncoder(w).Encode(githubapp.Token{Token: token}); err != nil {
						t.Error(err)
					}
				case "GET /repositories/701":
					if r.Header.Get("Authorization") != "token "+token {
						t.Error("repository request changed authorization")
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
			c := githubapp.New(githubapp.Config{AppID: 1, PrivateKey: key, APIURL: server.URL, HTTP: server.Client()})
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

func TestGitHubAppFakeInstallationTokenFormat(t *testing.T) {
	_, c := fixture(t)
	var previous string
	for range 2 {
		token, err := c.InstallationTokenByIDs(t.Context(), 7, []int64{701}, map[string]string{"contents": "read"})
		if err != nil {
			t.Fatal(err)
		}
		body, ok := strings.CutPrefix(token.Token, "ghs_"+strconv.Itoa(githubapptest.AppID)+"_")
		if !ok || len(token.Token) < 500 || len(token.Token) > 550 {
			t.Fatal("fake installation token must carry the App ID and be about 520 bytes")
		}
		segments := strings.Split(body, ".")
		if len(segments) != 3 {
			t.Fatal("fake installation token must have three segments")
		}
		for _, segment := range segments {
			decoded, err := base64.RawURLEncoding.DecodeString(segment)
			if err != nil || len(decoded) == 0 {
				t.Fatal("fake installation token segments must contain unpadded base64url data")
			}
		}
		if token.Token == previous {
			t.Fatal("fake installation tokens must be unique")
		}
		previous = token.Token
	}
}
