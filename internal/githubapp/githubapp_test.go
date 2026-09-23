package githubapp_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
)

func fixture(t *testing.T) (*githubapptest.GitHub, *githubapp.Client) {
	t.Helper()
	gh := githubapptest.New(t)
	gh.AddInstallation(githubapptest.Installation{
		ID:      7,
		Account: githubapp.Account{ID: 70, Login: "acme", Type: "Organization"},
		Repos: []githubapptest.Repo{
			{ID: 701, FullName: "acme/widgets"},
			{ID: 702, FullName: "acme/secret-plans", Private: true},
		},
	})
	return gh, githubapp.New(gh.Config())
}

func TestInstallationToken_CoversOnlyTheNamedRepository(t *testing.T) {
	gh, c := fixture(t)
	tok, err := c.InstallationToken(context.Background(), 7, []string{"widgets"}, map[string]string{"contents": "read"})
	if err != nil {
		t.Fatalf("InstallationToken: %v", err)
	}
	if !gh.TokenCovers(tok.Token, "acme/widgets") {
		t.Fatal("the token does not read the repository it was minted for")
	}
	if gh.TokenCovers(tok.Token, "acme/secret-plans") {
		t.Fatal("a token minted for acme/widgets reads acme/secret-plans")
	}
	minted := gh.Minted()
	if len(minted) != 1 || len(minted[0].Repositories) != 1 || minted[0].Permissions["contents"] != "read" || len(minted[0].Permissions) != 1 {
		t.Fatalf("the token request asked for %+v, want one repository and contents:read only", minted)
	}
}

func TestInstallationToken_RepositoryOutsideTheInstallationIsNotInstalled(t *testing.T) {
	_, c := fixture(t)
	_, err := c.InstallationToken(context.Background(), 7, []string{"elsewhere"}, map[string]string{"contents": "read"})
	if !errors.Is(err, githubapp.ErrNotInstalled) {
		t.Fatalf("err = %v, want ErrNotInstalled", err)
	}
}

func TestAppJWT_AnotherKeyIsRejected(t *testing.T) {
	gh, _ := fixture(t)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cfg := gh.Config()
	cfg.PrivateKey = other
	_, err = githubapp.New(cfg).Installation(context.Background(), 7)
	if !errors.Is(err, githubapp.ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected for a JWT signed by another key", err)
	}
	if _, err := githubapp.New(gh.Config()).Installation(context.Background(), 7); err != nil {
		t.Fatalf("control: the App's own key was refused: %v", err)
	}
}

func TestVerifyWebhook(t *testing.T) {
	_, c := fixture(t)
	body := []byte(`{"action":"opened"}`)
	if !c.VerifyWebhook(githubapptest.Sign(body), body) {
		t.Fatal("a delivery signed with the webhook secret did not verify")
	}
	if c.VerifyWebhook(githubapptest.SignWith("other", body), body) {
		t.Fatal("a delivery signed with another secret verified")
	}
	if c.VerifyWebhook(githubapptest.Sign(body), []byte(`{"action":"closed"}`)) {
		t.Fatal("a signature verified a body it did not cover")
	}
}

func TestParsePrivateKey_AcceptsPKCS1AndPKCS8(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	for name, text := range map[string][]byte{"pkcs1": pkcs1, "pkcs8": pkcs8} {
		got, err := githubapp.ParsePrivateKey(text)
		if err != nil || !got.Equal(key) {
			t.Errorf("%s: got %v, %v", name, got != nil, err)
		}
	}
	if _, err := githubapp.ParsePrivateKey([]byte("not a key")); err == nil {
		t.Error("text that is not PEM parsed")
	}
}
