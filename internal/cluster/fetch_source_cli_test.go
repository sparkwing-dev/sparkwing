package cluster

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
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
			os.MkdirAll(filepath.Join(d, ".sparkwing"), 0o755),
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
	goEnv, err := os.ReadFile(filepath.Join(filepath.Dir(dest), orchestrator.GoEnvFile))
	if err != nil || string(goEnv) != "GOPRIVATE=github.com/acme/plans\n" {
		t.Fatalf("the pipeline's module settings = %q, %v", goEnv, err)
	}
}

// A token left in any file on the shared volume, an LFS log or a module cache
// entry alike, fails the pod before the pipeline's container starts.
func TestFetchSource_FailsWhenTheTokenIsLeftOnTheVolume(t *testing.T) {
	for name, plant := range map[string]func(dest, cache string) string{
		"lfs log": func(dest, _ string) string { return filepath.Join(dest, ".git", "lfs", "logs", "20260929.log") },
		"module cache": func(_, cache string) string {
			return filepath.Join(cache, "cache", "download", "github.com", "acme", "plans", "@v", "v1.0.0.info")
		},
	} {
		volume := t.TempDir()
		dest, cache := filepath.Join(volume, "src"), filepath.Join(volume, "go-mod")
		path := plant(dest, cache)
		checkout := func(_ context.Context, _, _, _, d string, _ bincache.DirectCredential, _ bincache.SourceOptions, _ []string) error {
			return errors.Join(os.MkdirAll(filepath.Join(d, ".git"), 0o755), os.MkdirAll(filepath.Join(d, ".sparkwing"), 0o755), os.MkdirAll(filepath.Dir(path), 0o755),
				os.WriteFile(filepath.Join(d, "go.mod"), []byte("module x\n"), 0o644),
				os.WriteFile(path, []byte(strings.Repeat("x", (1<<20)-3)+fetchTok+"\n"), 0o644))
		}
		err := fetchSource(context.Background(), fakeSourceCredentials{sourceCredential()}, "run-1", dest, cache,
			checkout, func(context.Context, string, []string) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "holds the source credential") {
			t.Fatalf("%s: err = %v, want the pod failed", name, err)
		}
	}
}

// A run whose owner listed no repository has no private module to download.
func TestFetchSource_DownloadsNothingWithoutAListedRepository(t *testing.T) {
	sc := sourceCredential()
	sc.Repositories = sc.Repositories[:1]
	called := false
	err := fetchSource(context.Background(), fakeSourceCredentials{sc}, "run-1", filepath.Join(t.TempDir(), "src"), t.TempDir(),
		func(_ context.Context, _, _, _, d string, _ bincache.DirectCredential, _ bincache.SourceOptions, _ []string) error {
			return os.MkdirAll(filepath.Join(d, ".sparkwing"), 0o755)
		},
		func(context.Context, string, []string) error { called = true; return nil })
	if err != nil || called {
		t.Fatalf("err = %v, downloaded = %v", err, called)
	}
}

func TestFetchSource_RejectsSymlinkedModuleSourcesBeforeDownload(t *testing.T) {
	for _, tc := range []struct {
		name string
		link string
	}{
		{"pipeline directory", ".sparkwing"},
		{"root module", "go.mod"},
		{"pipeline module", filepath.Join(".sparkwing", "go.mod")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			volume := t.TempDir()
			dest := filepath.Join(volume, "src")
			outside := filepath.Join(t.TempDir(), "outside")
			checkout := func(_ context.Context, _, _, _, d string, _ bincache.DirectCredential, _ bincache.SourceOptions, _ []string) error {
				if err := os.MkdirAll(d, 0o755); err != nil {
					return err
				}
				if tc.link != ".sparkwing" {
					if err := os.MkdirAll(filepath.Join(d, ".sparkwing"), 0o755); err != nil {
						return err
					}
				}
				if tc.link == ".sparkwing" {
					if err := os.Mkdir(outside, 0o755); err != nil {
						return err
					}
				} else if err := os.WriteFile(outside, []byte("module outside\n"), 0o644); err != nil {
					return err
				}
				return os.Symlink(outside, filepath.Join(d, tc.link))
			}
			called := false
			err := fetchSource(context.Background(), fakeSourceCredentials{sourceCredential()}, "run-1", dest, filepath.Join(volume, "go-mod"),
				checkout, func(context.Context, string, []string) error { called = true; return nil })
			if err == nil || !strings.Contains(err.Error(), tc.link) || called {
				t.Fatalf("err = %v, downloaded = %v", err, called)
			}
		})
	}
}

type flakySourceCredentials struct {
	errs  []error
	calls int
}

func (f *flakySourceCredentials) SourceCredential(context.Context, string) (*store.SourceCredential, error) {
	f.calls++
	if f.calls <= len(f.errs) && f.errs[f.calls-1] != nil {
		return nil, f.errs[f.calls-1]
	}
	return sourceCredential(), nil
}

// A mint that failed on the network or at the controller is asked for again,
// up to the claim's allowance; a refusal is never retried.
func TestAskSourceCredential_RetriesOnlyTransientFailures(t *testing.T) {
	was := sourceMintBackoff
	sourceMintBackoff = 0
	t.Cleanup(func() { sourceMintBackoff = was })
	netErr := &url.Error{Op: "Post", URL: "http://controller", Err: errors.New("connection reset")}
	serverErr := fmt.Errorf("%w: controller 502: GitHub did not issue a token", client.ErrControllerFailed)
	refused := errors.New("controller 403: source_credential_spent")
	for name, tc := range map[string]struct {
		errs      []error
		calls     int
		succeeded bool
	}{
		"recovers after two failures": {[]error{netErr, serverErr}, 3, true},
		"stops at the allowance":      {[]error{serverErr, netErr, serverErr, nil}, store.MaxSourceMints, false},
		"never retries a refusal":     {[]error{refused, nil}, 1, false},
	} {
		f := &flakySourceCredentials{errs: tc.errs}
		sc, err := askSourceCredential(context.Background(), f, "run-1")
		if f.calls != tc.calls || (err == nil) != tc.succeeded || (sc != nil) != tc.succeeded {
			t.Fatalf("%s: %d calls, %v, %v; want %d calls and success %v", name, f.calls, sc, err, tc.calls, tc.succeeded)
		}
	}
}

type recordingReporter struct{ reports []store.AttemptReport }

func (r *recordingReporter) ReportAttempt(_ context.Context, _, _ string, report store.AttemptReport) error {
	r.reports = append(r.reports, report)
	return nil
}

// A failed fetch reports its attempt at once: a refused credential as a failure
// no attempt can get past, and anything else as one a retry may.
func TestFetchSource_ReportsAFailedFetchByWhetherARetryCanPass(t *testing.T) {
	refused := &flakySourceCredentials{errs: []error{errors.New("controller 404: no_source_credential")}}
	checkoutFailed := func(context.Context, string, string, string, string, bincache.DirectCredential,
		bincache.SourceOptions, []string,
	) error {
		return errors.New("git fetch: connection reset")
	}
	for name, tc := range map[string]struct {
		creds  sourceCredentials
		reason string
	}{
		"refused credential": {refused, store.FailureSourceUnavailable},
		"checkout failure":   {fakeSourceCredentials{sourceCredential()}, store.FailureSourceFetch},
	} {
		dest := filepath.Join(t.TempDir(), "src")
		cause := fetchSource(context.Background(), tc.creds, "run-1", dest, t.TempDir(), checkoutFailed,
			func(context.Context, string, []string) error { return nil })
		if cause == nil {
			t.Fatalf("%s: the fetch succeeded", name)
		}
		rep := &recordingReporter{}
		if err := reportFetchFailure(context.Background(), rep, "run-1", "plan", cause); !errors.Is(err, cause) {
			t.Fatalf("%s: err = %v, want the fetch's own error", name, err)
		}
		if len(rep.reports) != 1 || rep.reports[0].Outcome != "failed" || rep.reports[0].FailureReason != tc.reason {
			t.Fatalf("%s: reports = %+v, want one failed attempt with reason %q", name, rep.reports, tc.reason)
		}
	}
}
