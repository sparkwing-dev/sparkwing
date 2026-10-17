package source_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

type freshBundleAccess struct{ access }

func (*freshBundleAccess) BundleURL(context.Context, string) (string, error) {
	return "https://objects.example/source", nil
}

type freshBundleTransport struct{ file string }

func (b freshBundleTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, err := os.Open(b.file)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: body, Header: http.Header{}, Request: r}, nil
}

func TestSourceBundleCreatesFreshHostWorkspace(t *testing.T) {
	repo, sha := sourcetest.Repository(t)
	bundle := filepath.Join(t.TempDir(), "source.bundle")
	sourcetest.Git(t, repo, "bundle", "create", bundle, "HEAD")
	file, err := os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, file)
	if err := file.Close(); err != nil || copyErr != nil {
		t.Fatalf("hash bundle: %v %v", copyErr, err)
	}
	workspace := filepath.Join(t.TempDir(), "fresh-home", "workspaces")
	entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "git:example.test/acme/repo", RemoteURL: "https://example.test/acme/repo", SHA: sha, BundleDigest: hex.EncodeToString(digest.Sum(nil))}, source.Options{
		Workspace: workspace, Access: &freshBundleAccess{}, BundleTransport: freshBundleTransport{file: bundle}, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment,
	})
	if err != nil {
		t.Fatalf("fresh registered machine could not acquire submitted source: %v", err)
	}
	if entry.SHA != sha {
		t.Fatalf("source revision=%s, want %s", entry.SHA, sha)
	}
	if err := entry.Close(); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(workspace); err != nil || len(entries) != 0 {
		t.Fatalf("source acquisition left owned workspaces: %v %v", entries, err)
	}
}
