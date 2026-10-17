package source_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

type deniedSourceAccess struct {
	access
	signed int
}

func (a *deniedSourceAccess) BundleURL(context.Context, string) (string, error) {
	a.signed++
	return "", errors.New("unauthorized bundle signing")
}

func FuzzSourceAuthorizationPrecedesBytes(f *testing.F) {
	f.Add("git:8.8.8.8/team/repo", "revoked", true)
	f.Add("gh:42", "not allowed", false)
	f.Fuzz(func(t *testing.T, repo, refusal string, bundled bool) {
		if repo == "" {
			return
		}
		denied := errors.New(refusal)
		a := &deniedSourceAccess{access: access{err: denied}}
		spec := source.Spec{RepoKey: repo, RemoteURL: "https://8.8.8.8/team/repo", SHA: strings.Repeat("a", 40)}
		if bundled {
			spec.BundleDigest = strings.Repeat("b", 64)
		}
		workspace := t.TempDir()
		entry, err := source.PrepareCheckout(t.Context(), spec, source.Options{Workspace: workspace, Access: a, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment})
		if entry != nil {
			_ = entry.Close()
		}
		if entry != nil || !errors.Is(err, denied) || a.calls != 1 || a.signed != 0 {
			t.Fatalf("denied source: entry=%v error=%v authorizations=%d signatures=%d", entry, err, a.calls, a.signed)
		}
		if entries, err := os.ReadDir(workspace); err != nil || len(entries) != 0 {
			t.Fatalf("denied source created workspace: %v: %v", entries, err)
		}
	})
}
