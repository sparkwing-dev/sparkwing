package githubapp_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
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

func TestCheckRuns_NeedTheChecksPermission(t *testing.T) {
	gh, c := fixture(t)
	ctx := context.Background()
	run := githubapp.CheckRun{Name: "sparkwing/build", HeadSHA: "0123456789abcdef0123456789abcdef01234567", Status: "queued"}

	statuses, err := c.InstallationToken(ctx, 7, []string{"widgets"}, map[string]string{"statuses": "write"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateCheckRun(ctx, statuses.Token, "acme", "widgets", run); !errors.Is(err, githubapp.ErrPermissionMissing) {
		t.Fatalf("create with a token lacking checks:write = %v, want ErrPermissionMissing", err)
	}

	gh.SetChecksGranted(7, false)
	if _, err := c.InstallationToken(ctx, 7, []string{"widgets"}, map[string]string{"checks": "write"}); !errors.Is(err, githubapp.ErrPermissionMissing) {
		t.Fatalf("checks token on an installation without the permission = %v, want ErrPermissionMissing", err)
	}
	if _, err := c.InstallationToken(ctx, 7, []string{"elsewhere"}, map[string]string{"statuses": "write"}); !errors.Is(err, githubapp.ErrNotInstalled) {
		t.Fatalf("control: a repository outside the installation = %v, want ErrNotInstalled", err)
	}

	gh.SetChecksGranted(7, true)
	checks, err := c.InstallationToken(ctx, 7, []string{"widgets"}, map[string]string{"checks": "write"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.CreateCheckRun(ctx, checks.Token, "acme", "widgets", run)
	if err != nil || id <= 0 {
		t.Fatalf("create = %d, %v", id, err)
	}
	run.Status, run.Conclusion = "completed", "success"
	if err := c.UpdateCheckRun(ctx, checks.Token, "acme", "widgets", id, run); err != nil {
		t.Fatalf("update: %v", err)
	}
	gh.FailCheckRuns(1)
	var apiErr *githubapp.APIError
	if err := c.UpdateCheckRun(ctx, checks.Token, "acme", "widgets", id, run); !errors.As(err, &apiErr) || !apiErr.Temporary() {
		t.Fatalf("update GitHub answered 502 = %v, want a temporary APIError", err)
	}
	calls := gh.CheckRunCalls()
	if len(calls) != 2 || calls[1].Method != "update" || calls[1].ID != id || calls[1].Conclusion != "success" {
		t.Fatalf("check run writes = %+v", calls)
	}
}

func TestInstallationRepositories_NamesACutList(t *testing.T) {
	for _, tc := range []struct {
		count     int
		truncated bool
	}{{1000, false}, {1001, true}} {
		gh, client := fixture(t)
		repos := make([]githubapptest.Repo, tc.count)
		for i := range repos {
			repos[i] = githubapptest.Repo{ID: int64(10000 + i), FullName: fmt.Sprintf("acme/r%d", i)}
		}
		gh.SetRepos(7, repos...)
		got, err := client.InstallationRepositories(context.Background(), 7)
		if tc.truncated {
			if !errors.Is(err, githubapp.ErrTooManyRepositories) || len(got) != 1000 {
				t.Fatalf("%d repositories = %d listed, %v; want 1000 with ErrTooManyRepositories", tc.count, len(got), err)
			}
			continue
		}
		if err != nil || len(got) != tc.count || got[tc.count-1].ID != int64(10000+tc.count-1) {
			t.Fatalf("%d repositories = %d listed, %v", tc.count, len(got), err)
		}
	}
}

func TestFileAtCommit_ReadsAtMostOneByteBeyondTheConfigurationLimit(t *testing.T) {
	gh, c := fixture(t)
	sha := strings.Repeat("a", 40)
	for _, size := range []int64{limits.ProjectConfigBytes, 3 * limits.ProjectConfigBytes} {
		body := bytes.Repeat([]byte{'x'}, int(size))
		gh.SetFile("acme/widgets", sha, ".sparkwing/sparkwing.yaml", body)
		got, found, err := c.FileAtCommit(context.Background(), 7, 701, "acme", "widgets", sha, ".sparkwing/sparkwing.yaml")
		want := body[:min(size, limits.ProjectConfigBytes+1)]
		if err != nil || !found || !bytes.Equal(got, want) {
			t.Fatalf("size %d: read %d bytes, found=%v, %v", size, len(got), found, err)
		}
	}
	if _, found, err := c.FileAtCommit(context.Background(), 7, 701, "acme", "widgets", sha, ".sparkwing/absent.yaml"); err != nil || found {
		t.Fatalf("absent file = %v, %v", found, err)
	}
}

func TestVerifyWebhook_AcceptsThePreviousSecretDuringRotation(t *testing.T) {
	gh, _ := fixture(t)
	body := []byte(`{"action":"opened"}`)
	for _, previous := range []string{"", "rotated-out-secret"} {
		cfg := gh.Config()
		cfg.PreviousWebhookSecret = previous
		c := githubapp.New(cfg)
		if !c.VerifyWebhook(githubapptest.Sign(body), body) {
			t.Fatal("the current secret did not verify")
		}
		if got := c.VerifyWebhook(githubapptest.SignWith("rotated-out-secret", body), body); got != (previous != "") {
			t.Fatalf("previous secret %q verified = %v", previous, got)
		}
		for _, other := range []string{"", "other"} {
			if c.VerifyWebhook(githubapptest.SignWith(other, body), body) {
				t.Fatalf("a delivery signed with %q verified", other)
			}
		}
	}
}
