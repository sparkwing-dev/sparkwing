package githubapp_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

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

func TestResolveCommitRestrictsTheTokenToTheNamedRepository(t *testing.T) {
	gh, c := fixture(t)
	const sha = "0123456789abcdef0123456789abcdef01234567"
	gh.SetCommit("acme/widgets", "heads/main", sha)
	got, err := c.ResolveCommit(context.Background(), 7, "acme", "widgets", "refs/heads/main")
	if err != nil || got != sha {
		t.Fatalf("ResolveCommit = %q, %v, want %q", got, err, sha)
	}
	minted := gh.Minted()
	if len(minted) != 1 || len(minted[0].Repositories) != 1 || minted[0].Repositories[0] != "widgets" || len(minted[0].RepositoryIDs) != 0 || minted[0].Permissions["contents"] != "read" || len(minted[0].Permissions) != 1 {
		t.Fatalf("the token request asked for %+v, want one repository and contents:read only", minted)
	}
}

func TestResolveCommitRefusesARepositoryOutsideTheInstallation(t *testing.T) {
	_, c := fixture(t)
	_, err := c.ResolveCommit(context.Background(), 7, "acme", "elsewhere", "refs/heads/main")
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

func TestWebhookMAC(t *testing.T) {
	c := githubapp.New(githubapp.Config{WebhookSecret: "current"})
	body := []byte(`{"action":"opened"}`)
	mac := c.WebhookMAC()
	_, _ = mac.Write(body)
	if !mac.Verify(githubapptest.SignWith("current", body)) {
		t.Fatal("a delivery signed with the webhook secret did not verify")
	}
	if mac.Verify(githubapptest.SignWith("other", body)) {
		t.Fatal("a delivery signed with another secret verified")
	}
	mac = c.WebhookMAC()
	_, _ = mac.Write([]byte(`{"action":"closed"}`))
	if mac.Verify(githubapptest.SignWith("current", body)) {
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

	statuses, err := c.InstallationTokenByIDs(ctx, 7, []int64{701}, map[string]string{"statuses": "write"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateCheckRun(ctx, statuses.Token, "acme", "widgets", run); !errors.Is(err, githubapp.ErrPermissionMissing) {
		t.Fatalf("create with a token lacking checks:write = %v, want ErrPermissionMissing", err)
	}

	gh.SetChecksGranted(7, false)
	if _, err := c.InstallationTokenByIDs(ctx, 7, []int64{701}, map[string]string{"checks": "write"}); !errors.Is(err, githubapp.ErrPermissionMissing) {
		t.Fatalf("checks token on an installation without the permission = %v, want ErrPermissionMissing", err)
	}
	if _, err := c.InstallationTokenByIDs(ctx, 7, []int64{999}, map[string]string{"statuses": "write"}); !errors.Is(err, githubapp.ErrNotInstalled) {
		t.Fatalf("control: a repository outside the installation = %v, want ErrNotInstalled", err)
	}

	gh.SetChecksGranted(7, true)
	checks, err := c.InstallationTokenByIDs(ctx, 7, []int64{701}, map[string]string{"checks": "write"})
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
	var apiErr interface{ Temporary() bool }
	if err := c.UpdateCheckRun(ctx, checks.Token, "acme", "widgets", id, run); !errors.As(err, &apiErr) || !apiErr.Temporary() {
		t.Fatalf("update GitHub answered 502 = %v, want a temporary error", err)
	}
	calls := gh.CheckRunCalls()
	if len(calls) != 2 || calls[1].Method != "update" || calls[1].ID != id || calls[1].Conclusion != "success" {
		t.Fatalf("check run writes = %+v", calls)
	}
}

func TestInstallationRepositoriesListsEveryPage(t *testing.T) {
	for _, count := range []int{1000, 1001, 10001} {
		key, err := rsa.GenerateKey(rand.Reader, 1024)
		if err != nil {
			t.Fatal(err)
		}
		client := githubapp.New(githubapp.Config{AppID: 1, PrivateKey: key, HTTP: &http.Client{Transport: refTransport(func(r *http.Request) (*http.Response, error) {
			body := `{"token":"metadata"}`
			if r.URL.Path == "/installation/repositories" {
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				repos := []githubapp.Repository{}
				for i := (page - 1) * 100; i < min(page*100, count); i++ {
					repos = append(repos, githubapp.Repository{ID: int64(10000 + i), FullName: fmt.Sprintf("acme/r%d", i)})
				}
				encoded, err := json.Marshal(map[string]any{"total_count": count, "repositories": repos})
				if err != nil {
					t.Fatal(err)
				}
				body = string(encoded)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})}})
		got, err := client.InstallationRepositories(context.Background(), 7)
		if err != nil || len(got) != count || got[count-1].ID != int64(10000+count-1) {
			t.Fatalf("%d repositories = %d listed, %v", count, len(got), err)
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

func TestWebhookMAC_AcceptsThePreviousSecretDuringRotation(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	for _, previous := range []string{"", "rotated-out-secret"} {
		c := githubapp.New(githubapp.Config{WebhookSecret: "current", PreviousWebhookSecret: previous})
		mac := c.WebhookMAC()
		_, _ = mac.Write(body)
		if !mac.Verify(githubapptest.SignWith("current", body)) {
			t.Fatal("the current secret did not verify")
		}
		if got := mac.Verify(githubapptest.SignWith("rotated-out-secret", body)); got != (previous != "") {
			t.Fatalf("previous secret %q verified = %v", previous, got)
		}
		for _, other := range []string{"", "other"} {
			if mac.Verify(githubapptest.SignWith(other, body)) {
				t.Fatalf("a delivery signed with %q verified", other)
			}
		}
	}
}

func TestInstallationRepositoriesRejectsNonAdvancingPages(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"repeated", "reordered", "overlapping", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			pages := 0
			client := githubapp.New(githubapp.Config{AppID: 1, PrivateKey: key, HTTP: &http.Client{Transport: refTransport(func(r *http.Request) (*http.Response, error) {
				body := `{"token":"metadata"}`
				if r.URL.Path == "/installation/repositories" {
					pages++
					if pages > 2 {
						return nil, errors.New("listing kept going after a non-advancing page")
					}
					repos := make([]githubapp.Repository, 100)
					for i := range repos {
						repos[i].ID = int64(i + 1)
					}
					if pages == 2 {
						switch mode {
						case "reordered":
							for i := range repos {
								repos[i].ID = int64(100 - i)
							}
						case "overlapping":
							for i := range repos {
								repos[i].ID += 99
							}
						case "truncated":
							repos = nil
						}
					}
					encoded, err := json.Marshal(map[string]any{"repositories": repos, "total_count": 300})
					if err != nil {
						t.Fatal(err)
					}
					body = string(encoded)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}})
			got, err := client.InstallationRepositories(t.Context(), 7)
			want := "did not advance"
			if mode == "truncated" {
				want = "listing stopped"
			}
			if got != nil || err == nil || !strings.Contains(err.Error(), want) || pages != 2 {
				t.Fatalf("listing = %d repositories, %v, %d pages; want explicit %q failure on page two", len(got), err, pages, want)
			}
		})
	}
}

func TestInstallationRepositoriesHasOverallDeadline(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		client := githubapp.New(githubapp.Config{AppID: 1, PrivateKey: key, HTTP: &http.Client{Transport: refTransport(func(r *http.Request) (*http.Response, error) {
			timer := time.NewTimer(9 * time.Second)
			defer timer.Stop()
			select {
			case <-r.Context().Done():
				return nil, r.Context().Err()
			case <-timer.C:
			}
			body := `{"token":"metadata"}`
			if r.URL.Path == "/installation/repositories" {
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				repos := []githubapp.Repository{}
				if page <= 20 {
					for i := range 100 {
						repos = append(repos, githubapp.Repository{ID: int64((page-1)*100 + i + 1)})
					}
				}
				encoded, err := json.Marshal(map[string]any{"repositories": repos, "total_count": 2000})
				if err != nil {
					t.Fatal(err)
				}
				body = string(encoded)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})}})
		started := time.Now()
		got, err := client.InstallationRepositories(t.Context(), 7)
		if got != nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(started) != time.Minute {
			t.Fatalf("listing = %d repositories, %v, elapsed %s; want one overall deadline", len(got), err, time.Since(started))
		}
	})
}
