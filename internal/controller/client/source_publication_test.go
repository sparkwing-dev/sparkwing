package client_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

type publicationTransport func(*http.Request) (*http.Response, error)

func (f publicationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func FuzzSourcePublicationRejectsKnownSecretsBeforeNetwork(f *testing.F) {
	f.Add("opaque-private-secret", uint8(0))
	f.Add("opaque-private-secret", uint8(1))
	f.Add("chunk-boundary-private", uint8(2))
	f.Add("historical-private-secret", uint8(3))
	f.Fuzz(func(t *testing.T, value string, form uint8) {
		if len(value) < 8 || len(value) > 1000 {
			t.Skip()
		}
		repo, _ := sourcetest.Repository(t)
		body := value
		switch form % 4 {
		case 1:
			body = base64.StdEncoding.EncodeToString([]byte(value))
		case 2:
			body = strings.Repeat("x", (64<<10)-len(value)/2) + value
		}
		sourcetest.Write(t, filepath.Join(repo, "ordinary.txt"), body)
		sourcetest.Git(t, repo, "add", ".")
		sourcetest.Git(t, repo, "commit", "-qm", "included secret")
		if form%4 == 3 {
			sourcetest.Write(t, filepath.Join(repo, "ordinary.txt"), "clean")
			sourcetest.Git(t, repo, "add", ".")
			sourcetest.Git(t, repo, "commit", "-qm", "removed from latest tree")
		}
		path := filepath.Join(t.TempDir(), "source.bundle")
		sourcetest.Git(t, repo, "bundle", "create", path, "HEAD")
		requests := 0
		options := client.EngineOptions{URL: "https://controller.example", HTTP: &http.Client{Transport: publicationTransport(func(*http.Request) (*http.Response, error) {
			requests++
			return nil, errors.New("unexpected publication")
		})}}
		_, err := client.UploadSourceSnapshot(t.Context(), options, source.Publication{Path: path, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t), Masker: secrets.NewMasker()}, Secrets: map[string]string{"SIGNING_MATERIAL": value}})
		if err == nil || !strings.Contains(err.Error(), "source bundle contains a declared local secret") || requests != 0 {
			t.Fatalf("secret reached publication: requests=%d err=%v", requests, err)
		}
		if strings.Contains(err.Error(), value) || strings.Contains(err.Error(), "ordinary.txt") {
			t.Fatal("secret rejection exposed private contents")
		}
	})
}

func TestSourcePublicationRequiresPreparedClassification(t *testing.T) {
	for _, publication := range []source.Publication{
		{},
		{Base: procenv.Base{Masker: secrets.NewMasker()}},
		{Secrets: map[string]string{}},
	} {
		_, err := client.UploadSourceSnapshot(context.Background(), client.EngineOptions{URL: "https://controller.example"}, publication)
		if err == nil || !strings.Contains(err.Error(), "prepared secret classification") {
			t.Fatalf("missing classification accepted: %v", err)
		}
	}
}

func FuzzSourcePublicationRejectsSecretMetadata(f *testing.F) {
	for form := range uint8(10) {
		f.Add("opaque4f1c9e", form)
	}
	f.Fuzz(func(t *testing.T, input string, form uint8) {
		sum := sha256.Sum256([]byte(input))
		value := "opaque" + hex.EncodeToString(sum[:8])
		repo, _ := sourcetest.Repository(t)
		git, code := runspec.Git{}, runspec.Source{}
		selector := "HEAD"
		switch form % 10 {
		case 0:
			sourcetest.Write(t, filepath.Join(repo, value+".go"), "package ordinary\n")
			sourcetest.Git(t, repo, "add", ".")
			sourcetest.Git(t, repo, "commit", "-qm", "harmless file")
		case 1:
			sourcetest.Git(t, repo, "commit", "--allow-empty", "-qm", "message "+value)
		case 2:
			sourcetest.Git(t, repo, "tag", "-a", "release", "-m", "message "+value)
			selector = "release"
		case 3:
			selector = "refs/heads/" + value
			sourcetest.Git(t, repo, "branch", value)
		case 4:
			selector = "refs/tags/" + value
			sourcetest.Git(t, repo, "tag", value)
		case 5:
			sourcetest.Git(t, repo, "commit", "--allow-empty", "-qm", "base "+value)
			sourcetest.Git(t, repo, "commit", "--allow-empty", "-qm", "clean tip")
		case 6:
			git.Ref = "refs/heads/" + value
		case 7:
			code.RequestedRef = "refs/tags/" + value
		case 8:
			code.Location.PipelineDir = value
		case 9:
			value = "<\"" + value + ">\\\n"
			code.Entrypoint = value
		}
		path := filepath.Join(t.TempDir(), "source.bundle")
		args := []string{"bundle", "create", path, selector}
		if form%10 == 5 {
			args = append(args, "^HEAD~1")
		}
		sourcetest.Git(t, repo, args...)
		requests := 0
		options := client.EngineOptions{URL: "https://controller.example", HTTP: &http.Client{Transport: publicationTransport(func(*http.Request) (*http.Response, error) {
			requests++
			return nil, errors.New("unexpected publication")
		})}}
		_, err := client.UploadSourceSnapshot(t.Context(), options, source.Publication{Git: git, Source: code, Path: path, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t), Masker: secrets.NewMasker()}, Secrets: map[string]string{"SIGNING_MATERIAL": value}})
		if err == nil || !strings.Contains(err.Error(), "source bundle contains a declared local secret") || requests != 0 {
			t.Fatalf("secret metadata reached publication: form=%d requests=%d err=%v", form%10, requests, err)
		}
		if strings.Contains(err.Error(), value) {
			t.Fatal("secret rejection exposed private metadata")
		}
	})
}

func TestSourcePublicationScansAThinBundleAgainstItsCheckout(t *testing.T) {
	const secret = "history-only-private-value"
	for name, newCommitHoldsSecret := range map[string]bool{"secret in new commit": true, "secret only in pushed history": false} {
		t.Run(name, func(t *testing.T) {
			repo, _ := sourcetest.Repository(t)
			sourcetest.Write(t, filepath.Join(repo, "pushed.txt"), map[bool]string{true: "clean", false: secret}[newCommitHoldsSecret])
			sourcetest.Git(t, repo, "add", ".")
			sourcetest.Git(t, repo, "commit", "-qm", "pushed")
			sourcetest.Write(t, filepath.Join(repo, "pushed.txt"), "clean again")
			sourcetest.Git(t, repo, "commit", "-qam", "pushed cleanup")
			sourcetest.Git(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
			sourcetest.Write(t, filepath.Join(repo, "local.txt"), map[bool]string{true: secret, false: "clean"}[newCommitHoldsSecret])
			sourcetest.Git(t, repo, "add", ".")
			sourcetest.Git(t, repo, "commit", "-qm", "local")
			path := filepath.Join(t.TempDir(), "source.bundle")
			sourcetest.Git(t, repo, "bundle", "create", path, "HEAD", "--not", "refs/remotes/origin/main")
			requests := 0
			options := client.EngineOptions{URL: "https://controller.example", HTTP: &http.Client{Transport: publicationTransport(func(*http.Request) (*http.Response, error) {
				requests++
				return nil, errors.New("publication reached the network")
			})}}
			publication := source.Publication{Path: path, Source: runspec.Source{Location: runspec.SourceLocation{LocalCheckout: repo}}, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t), Masker: secrets.NewMasker()}, Secrets: map[string]string{"TOKEN": secret}}
			_, err := client.UploadSourceSnapshot(t.Context(), options, publication)
			refused := err != nil && strings.Contains(err.Error(), "source bundle contains a declared local secret")
			if refused != newCommitHoldsSecret || refused == (requests != 0) {
				t.Fatalf("refused=%v requests=%d err=%v", refused, requests, err)
			}
		})
	}
}
