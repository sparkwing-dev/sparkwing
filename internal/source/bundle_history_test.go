package source_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

func TestBundleHistoryPolicyPreservesUnpushedExecutionAndCode(t *testing.T) {
	owner, _ := sourcetest.Repository(t)
	for _, name := range []string{"second", "execution", "code"} {
		sourcetest.Write(t, filepath.Join(owner, name), name)
		sourcetest.Git(t, owner, "add", name)
		sourcetest.Git(t, owner, "commit", "-qm", name)
		sourcetest.Git(t, owner, "tag", name)
	}
	execution := sourcetest.Git(t, owner, "rev-parse", "execution")
	code := sourcetest.Git(t, owner, "rev-parse", "code")
	bundle := filepath.Join(t.TempDir(), "source.bundle")
	sourcetest.Git(t, owner, "bundle", "create", bundle, "refs/tags/execution", "refs/tags/code")
	body, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	for _, local := range []bool{false, true} {
		for _, depth := range []int{1, 2, 0} {
			t.Run(fmt.Sprintf("local=%v/depth=%d", local, depth), func(t *testing.T) {
				spec := source.Spec{RepoKey: "git:example.test/acme/repo", RemoteURL: "https://example.test/acme/repo", SHA: execution, PipelineSHA: code, BundleDigest: hex.EncodeToString(digest[:]), DefaultBranch: "trunk", CommittedCheckout: true, Checkout: runspec.CheckoutPolicy{Policy: nodeproto.SourceSpec{Depth: new(depth)}}}
				options := source.Options{Workspace: t.TempDir(), Access: &freshBundleAccess{}, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BundleTransport: bundleResponse(func() io.ReadCloser { return io.NopCloser(bytes.NewReader(body)) })}
				if local {
					spec.RemoteURL, spec.LocalCheckout, spec.BundleDigest = "", owner, ""
					options.LocalReplica = true
					options.LocalBundle = bundle
				}
				entry, err := source.PrepareCheckout(t.Context(), spec, options)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := entry.Close(); err != nil {
						t.Error(err)
					}
				}()
				for directory, sha := range map[string]string{entry.CheckoutDirectory(): execution, filepath.Dir(entry.Directory): code} {
					if got := sourcetest.Git(t, directory, "rev-parse", "HEAD"); got != sha {
						t.Fatalf("HEAD=%s, want %s", got, sha)
					}
					want := depth
					if want == 0 {
						want = 3
						if sha == code {
							want = 4
						}
					}
					if got := sourcetest.Git(t, directory, "rev-list", "--count", "HEAD"); got != strconv.Itoa(want) {
						t.Fatalf("%s history=%s, want %d", sha, got, want)
					}
				}
			})
		}
	}
	if shallow := sourcetest.Git(t, owner, "rev-parse", "--is-shallow-repository"); shallow != "false" {
		t.Fatal("owner history changed")
	}
}
