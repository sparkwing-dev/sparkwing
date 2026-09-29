package cluster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type fakeSourceCredentials struct{ sc *store.SourceCredential }

func (f fakeSourceCredentials) SourceCredential(context.Context, string) (*store.SourceCredential, error) {
	return f.sc, nil
}

const fetchTok = "ghs_fetch_fixture"

func sourceCredential() *store.SourceCredential {
	return &store.SourceCredential{
		Token: fetchTok, RepoURL: "https://github.com/acme/widgets.git", SHA: strings.Repeat("a", 40), Branch: "main",
		Repositories: []string{"acme/widgets", "acme/plans"}, Source: store.SourceSpec{Depth: 0, Tags: true, Submodules: true},
	}
}

// The init container checks out what the controller named with the token it
// issued, lets submodules reach only the listed repositories, and downloads
// modules with go fetching only those directly.
func TestFetchSource_ChecksOutAndDownloadsWithTheIssuedToken(t *testing.T) {
	dest, cache := filepath.Join(t.TempDir(), "src"), t.TempDir()
	var got struct {
		cred bincache.DirectCredential
		o    bincache.SourceOptions
		subs []string
		dirs []string
		env  []string
	}
	checkout := func(_ context.Context, repoURL, sha, branch, d string, cred bincache.DirectCredential,
		o bincache.SourceOptions, subs []string,
	) error {
		got.cred, got.o, got.subs = cred, o, subs
		return errors.Join(os.MkdirAll(filepath.Join(d, ".git"), 0o755),
			os.WriteFile(filepath.Join(d, ".git", "config"), []byte("[remote \"origin\"]\n\turl = "+repoURL+"\n"), 0o644),
			os.WriteFile(filepath.Join(d, "go.mod"), []byte("module github.com/acme/widgets\n"), 0o644))
	}
	download := func(_ context.Context, dir string, env []string) error {
		got.dirs, got.env = append(got.dirs, dir), env
		return nil
	}
	if err := fetchSource(context.Background(), fakeSourceCredentials{sourceCredential()}, "run-1", dest, cache,
		checkout, download); err != nil {
		t.Fatal(err)
	}
	if got.cred.Kind != bincache.CredentialGitHubApp || got.cred.Secret != fetchTok ||
		got.o != (bincache.SourceOptions{Tags: true, Submodules: true}) ||
		!slices.Equal(got.subs, []string{"github.com/acme/plans"}) {
		t.Fatalf("checkout got %+v", got)
	}
	if !slices.Equal(got.dirs, []string{dest}) || !slices.Contains(got.env, "GOPRIVATE=github.com/acme/plans") ||
		!slices.Contains(got.env, "GOTOOLCHAIN=local") {
		t.Fatalf("download dirs %v env %v", got.dirs, got.env)
	}

	leaky := func(_ context.Context, _, _, _, d string, _ bincache.DirectCredential, _ bincache.SourceOptions, _ []string) error {
		return errors.Join(os.MkdirAll(filepath.Join(d, ".git"), 0o755),
			os.WriteFile(filepath.Join(d, ".git", "config"), []byte("[credential]\n\thelper = store\n"), 0o644))
	}
	err := fetchSource(context.Background(), fakeSourceCredentials{sourceCredential()}, "run-1",
		filepath.Join(t.TempDir(), "src"), cache, leaky, download)
	if err == nil || !strings.Contains(err.Error(), "holds a git credential") {
		t.Fatalf("a checkout left with a credential helper: err = %v", err)
	}
}

// A run whose owner listed no repository has no private module to download.
func TestFetchSource_DownloadsNothingWithoutAListedRepository(t *testing.T) {
	sc := sourceCredential()
	sc.Repositories = sc.Repositories[:1]
	called := false
	err := fetchSource(context.Background(), fakeSourceCredentials{sc}, "run-1", filepath.Join(t.TempDir(), "src"), t.TempDir(),
		func(_ context.Context, _, _, _, d string, _ bincache.DirectCredential, _ bincache.SourceOptions, _ []string) error {
			return os.MkdirAll(d, 0o755)
		},
		func(context.Context, string, []string) error { called = true; return nil })
	if err != nil || called {
		t.Fatalf("err = %v, downloaded = %v", err, called)
	}
}
