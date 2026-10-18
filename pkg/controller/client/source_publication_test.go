package client_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
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
		_, err := client.UploadSourceSnapshot(t.Context(), options, runspec.Git{}, runspec.Source{}, source.Publication{Path: path, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t), Masker: secrets.NewMasker()}, Secrets: map[string]string{"SIGNING_MATERIAL": value}})
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
		_, err := client.UploadSourceSnapshot(context.Background(), client.EngineOptions{URL: "https://controller.example"}, runspec.Git{}, runspec.Source{}, publication)
		if err == nil || !strings.Contains(err.Error(), "prepared secret classification") {
			t.Fatalf("missing classification accepted: %v", err)
		}
	}
}
