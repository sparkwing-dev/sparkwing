//go:build !windows

package nodehost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestCacheDirectoryQueriesToolsWithSealedJobEnvironment(t *testing.T) {
	for _, resolver := range []string{"gomod", "npm"} {
		t.Run(resolver, func(t *testing.T) {
			input, state, _, _ := executorFixture(t)
			input.Entry.Directory = t.TempDir()
			tools := filepath.Join(input.Entry.Directory, "tools")
			if err := os.Mkdir(tools, 0o755); err != nil {
				t.Fatal(err)
			}
			name := "go"
			if resolver == "npm" {
				name = "npm"
			}
			script := "#!/bin/sh\n[ -z \"$SPARKWING_CLAIM_TOKEN\" ] || exit 7\n[ \"$RESOLVER_INPUT\" = sealed ] || exit 8\nprintf '%s/cache' \"$PWD\"\n"
			if err := os.WriteFile(filepath.Join(tools, name), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			job := nodeproto.JobSpec{ID: input.NodeID, Env: map[string]nodeproto.EnvValue{"PATH": {Value: "tools"}, "RESOLVER_INPUT": {Value: "sealed"}}}
			state.row.Spec, _ = json.Marshal(job)
			environments := SDKEnvironment{Store: state, Base: procenv.Base{Ambient: []string{"SPARKWING_CLAIM_TOKEN=private-host-credential", "PATH=/nonexistent", "HOME=/nonexistent"}, Masker: secrets.NewMasker()}}
			dir, err := environments.CacheDirectory(t.Context(), input, nodeproto.CacheSpec{Resolver: resolver})
			if err != nil || dir != filepath.Join(input.Entry.Directory, "cache") {
				t.Fatalf("tool cache=%q, %v", dir, err)
			}
		})
	}
}
