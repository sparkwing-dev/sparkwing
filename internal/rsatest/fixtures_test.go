package rsatest_test

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth/googletest"
	"github.com/sparkwing-dev/sparkwing/internal/jwks/jwkstest"
)

func TestIdentityFixturesReuseKeysWithoutReusingProviderState(t *testing.T) {
	var appKey, signerKey *rsa.PrivateKey
	var googleN, oldCode, oldToken string
	for _, phase := range []string{"earlier", "later"} {
		t.Run(phase, func(t *testing.T) {
			app := githubapptest.New(t)
			signer := jwkstest.NewSigner(t, "test-key")
			google := googletest.New(t)
			response, err := http.Get(google.Config().JWKSURL)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var published struct{ Keys []struct{ N string } }
			if err := json.NewDecoder(response.Body).Decode(&published); err != nil {
				t.Fatal(err)
			}
			if len(published.Keys) != 1 {
				t.Fatalf("Google keys=%d", len(published.Keys))
			}
			n := published.Keys[0].N
			appN := base64.RawURLEncoding.EncodeToString(app.Key.N.Bytes())
			signerN := base64.RawURLEncoding.EncodeToString(signer.Key.N.Bytes())
			if appN == signerN || appN == n || signerN == n {
				t.Fatal("live identity fixtures shared key material")
			}
			client := googleauth.New(google.Config())
			if phase == "later" {
				if !app.Key.Equal(appKey) || !signer.Key.Equal(signerKey) || n != googleN {
					t.Fatal("released fixture keys were not reused")
				}
				if app.Key == appKey || signer.Key == signerKey {
					t.Fatal("new fixtures retained old mutable key objects")
				}
				if app.TokenCovers(oldToken, "acme/app") || len(app.Minted()) != 0 {
					t.Fatal("installation token state crossed fixture cleanup")
				}
				if _, err := client.Exchange(t.Context(), oldCode, "verifier", "https://test.example/callback"); !errors.Is(err, googleauth.ErrRejected) {
					t.Fatalf("old Google authorization code: %v", err)
				}
			}
			app.AddInstallation(githubapptest.Installation{ID: 7, Repos: []githubapptest.Repo{{ID: 42, FullName: "acme/app"}}})
			token, err := githubapp.New(app.Config()).InstallationTokenByIDs(t.Context(), 7, []int64{42}, map[string]string{"contents": "read"})
			if err != nil {
				t.Fatal(err)
			}
			if !app.TokenCovers(token.Token, "acme/app") {
				t.Fatal("new installation token was not registered")
			}
			code := google.Code(googletest.Person{Subject: phase, Email: phase + "@example.com", EmailVerified: true}, "verifier", "https://test.example/callback")
			before := google.KeyFetches()
			claims, err := client.Exchange(t.Context(), code, "verifier", "https://test.example/callback")
			if err != nil || claims.Subject != phase {
				t.Fatalf("new issuer's claims: %+v %v", claims, err)
			}
			if google.KeyFetches() != before+1 {
				t.Fatal("new client reused another fixture's JWKS cache")
			}
			if phase == "earlier" {
				appKey, signerKey, googleN, oldToken = app.Key, signer.Key, n, token.Token
				oldCode = google.Code(googletest.Person{Subject: "unredeemed", Email: "old@example.com", EmailVerified: true}, "verifier", "https://test.example/callback")
			}
		})
	}
}
