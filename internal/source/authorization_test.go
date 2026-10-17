package source_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	return "", a.err
}

func FuzzSourceAuthorizationPrecedesBytes(f *testing.F) {
	f.Add("git:8.8.8.8/team/repo", "revoked", true, true)
	f.Add("gh:42", "not allowed", false, true)
	f.Add("gh:42", "revoked", true, false)
	f.Add("gh:42", "not allowed", false, false)
	f.Fuzz(func(t *testing.T, repo, refusal string, bundled, fresh bool) {
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
		if fresh {
			workspace = filepath.Join(workspace, "new-workspace")
		}
		entry, err := source.PrepareCheckout(t.Context(), spec, source.Options{Workspace: workspace, Access: a, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment})
		if entry != nil {
			_ = entry.Close()
		}
		calls, signatures := 1, 0
		if bundled {
			calls, signatures = 0, 1
		}
		if entry != nil || !errors.Is(err, denied) || a.calls != calls || a.signed != signatures {
			t.Fatalf("denied source: entry=%v error=%v authorizations=%d signatures=%d", entry, err, a.calls, a.signed)
		}
		entries, err := os.ReadDir(workspace)
		if fresh && !os.IsNotExist(err) || !fresh && (err != nil || len(entries) != 0) {
			t.Fatalf("denied source created workspace: %v: %v", entries, err)
		}
	})
}
