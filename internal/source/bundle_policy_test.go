package source_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

type bundleStream struct {
	io.Reader
	closeErr error
}

func (b bundleStream) Close() error { return b.closeErr }

type bundleResponse func() io.ReadCloser

func (b bundleResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: b(), Request: r}, nil
}

func bundleFixture(t *testing.T, link ...string) (source.Spec, source.Options, []byte) {
	t.Helper()
	repo, sha := sourcetest.Repository(t)
	if len(link) > 0 {
		original := filepath.Join(repo, ".sparkwing")
		target := filepath.Join(repo, "pipeline-source")
		if link[0] != "internal" {
			target = filepath.Join(t.TempDir(), "pipeline-source")
		}
		if err := os.Rename(original, target); err != nil {
			t.Fatal(err)
		}
		if link[0] == "internal" {
			target = "pipeline-source"
		} else if link[0] == "relative-external" {
			var err error
			target, err = filepath.Rel(repo, target)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(target, original); err != nil {
			t.Fatal(err)
		}
		sourcetest.Git(t, repo, "add", ".")
		sourcetest.Git(t, repo, "commit", "-qm", "pipeline symlink")
		sha = sourcetest.Git(t, repo, "rev-parse", "HEAD")
	}
	file := filepath.Join(t.TempDir(), "source.bundle")
	sourcetest.Git(t, repo, "bundle", "create", file, "HEAD")
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	spec := source.Spec{RepoKey: "git:example.test/acme/repo", RemoteURL: "https://example.test/acme/repo", SHA: sha, BundleDigest: hex.EncodeToString(digest[:])}
	options := source.Options{Workspace: t.TempDir(), Access: &freshBundleAccess{}, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BundleTransport: bundleResponse(func() io.ReadCloser { return io.NopCloser(bytes.NewReader(body)) })}
	return spec, options, body
}

func TestBundleAcquisitionRefusesBadBytesAndJoinsCancellation(t *testing.T) {
	for _, fault := range []string{"corrupt", "short", "long", "close", "cancelled", "wrong commit"} {
		t.Run(fault, func(t *testing.T) {
			spec, options, body := bundleFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch fault {
			case "corrupt":
				body[0] ^= 0xff
			case "short":
				body = body[:len(body)-1]
			case "long":
				body = append(body, 0)
			case "cancelled":
				cancel()
			case "wrong commit":
				spec.SHA = "0123456789abcdef0123456789abcdef01234567"
			}
			downloads := 0
			options.BundleTransport = bundleResponse(func() io.ReadCloser {
				downloads++
				stream := bundleStream{Reader: bytes.NewReader(body)}
				if fault == "close" {
					stream.closeErr = errors.New("failed close")
				}
				return stream
			})
			entry, err := source.PrepareCheckout(ctx, spec, options)
			if entry != nil {
				_ = entry.Close()
				t.Fatal("invalid bundle returned an entry")
			}
			if err == nil || fault == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("bundle failure lost", err)
			}
			if fault == "cancelled" && downloads != 0 {
				t.Fatal("cancelled acquisition downloaded bytes")
			}
			entries, err := os.ReadDir(options.Workspace)
			if err != nil || len(entries) != 0 {
				t.Fatal("bundle failure left a workspace", entries, err)
			}
		})
	}
}

func FuzzBundleAcquisitionIgnoresAmbientGitControls(f *testing.F) {
	f.Add([]byte("trace-and-routing"))
	f.Add([]byte("config-and-filter"))
	f.Fuzz(func(t *testing.T, seed []byte) {
		spec, options, _ := bundleFixture(t)
		outside := t.TempDir()
		sum := sha256.Sum256(seed)
		path := filepath.Join(outside, hex.EncodeToString(sum[:]))
		for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_TRACE", "GIT_TRACE_CURL", "GIT_TRACE_PACKET", "GIT_TRACE2_EVENT", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"} {
			t.Setenv(name, path)
		}
		t.Setenv("GIT_ALLOW_PROTOCOL", "ext")
		t.Setenv("GIT_DEFAULT_HASH", "sha256")
		options.Base.Captured = append(os.Environ(), "HOME="+t.TempDir())
		entry, err := source.PrepareCheckout(t.Context(), spec, options)
		if err != nil {
			t.Fatal(err)
		}
		if entry.SHA != spec.SHA {
			t.Fatal("bundle changed identity")
		}
		if err := entry.Close(); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(outside)
		if err != nil || len(entries) != 0 {
			t.Fatal("ambient Git controls wrote files", entries, err)
		}
	})
}

func TestSourceBundleRefusesSymlinkedPipeline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires Windows developer mode")
	}
	for _, link := range []string{"internal", "external", "relative-external"} {
		t.Run(link, func(t *testing.T) {
			spec, options, _ := bundleFixture(t, link)
			entry, err := source.PrepareCheckout(t.Context(), spec, options)
			if err == nil {
				_ = entry.Close()
				t.Fatal("accepted symlinked pipeline")
			}
			entries, err := os.ReadDir(options.Workspace)
			if err != nil || len(entries) != 0 {
				t.Fatal("symlink refusal left files", entries, err)
			}
		})
	}
}

func TestSourceBundleImportsSHA1UnderSHA256DefaultAndKeepsOtherEntries(t *testing.T) {
	spec, options, _ := bundleFixture(t)
	options.Base.Captured = append(options.Base.Captured, "GIT_DEFAULT_HASH=sha256")
	options.BuildEnvironment = sourcetest.BuildEnvironment
	first, err := source.PrepareCheckout(t.Context(), spec, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := first.Close(); err != nil {
			t.Error(err)
		}
	})
	path := filepath.Join(first.Directory, "pipeline.go")
	if err := os.WriteFile(path, []byte("existing checkout"), 0o600); err != nil {
		t.Fatal(err)
	}
	next, err := source.PrepareCheckout(t.Context(), spec, options)
	if err != nil {
		t.Fatal(err)
	}
	if next.CheckoutDirectory() == first.CheckoutDirectory() {
		t.Fatal("acquisition reused another entry's checkout")
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "existing checkout" {
		t.Fatal("acquisition changed another entry", string(body), err)
	}
}
