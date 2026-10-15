package nodehost

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func FuzzSDKCleanupEnvironment(f *testing.F) {
	f.Add("\xc9", "0", false)
	for _, name := range []string{"BODY", "DOCKER_HOST", "NODE_OPTIONS", "GOMAXPROCS", "SPARKWING_JOB_ID", "API_TOKEN", "SPARKWING_NODE_TOKEN", "A=B", ""} {
		f.Add(name, "declared", false)
	}
	f.Fuzz(func(t *testing.T, name, value string, startup bool) {
		name, value = strings.ToValidUTF8(name, "�"), strings.ToValidUTF8(value, "�")
		input, st, _, _ := executorFixture(t)
		var spec runspec.Spec
		if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
			t.Fatal(err)
		}
		captured := []string{"CAPTURED=original", "API_TOKEN=private-base-credential"}
		spec.Request.Request.Env, _ = runspec.EnvironmentDigest(map[string]string{"CAPTURED": "original", "API_TOKEN": "private-base-credential"})
		input.Accepted.Spec, _ = json.Marshal(spec)
		job := nodeproto.JobSpec{ID: input.NodeID, Env: map[string]nodeproto.EnvValue{name: {Value: value, Startup: startup}}}
		st.row.Spec, _ = json.Marshal(job)
		policy := SDKEnvironment{Base: procenv.Base{Local: true, Captured: captured}, Store: st}
		env, complete, err := policy.jobEnvironments(t.Context(), procenv.Attempt, input.Accepted, input.NodeID, input.Generation, input.SpecHash, input.GrantedRunner, true)
		legacyEnv, legacyComplete, legacyErr := attemptEnvironments(SessionConfig{Env: captured, Runner: input.GrantedRunner}, &NodeRun{Run: &Run{ID: input.Accepted.RunID, Context: executorRunContext(spec)}, Plan: job})
		if (err == nil) != (legacyErr == nil) || !slices.Equal(env, legacyEnv) || !slices.Equal(complete, legacyComplete) {
			t.Fatal("host entry points disagree on their environment projection")
		}
		if err != nil {
			if len(env) != 0 || len(complete) != 0 {
				t.Fatal("refused projection returned a partial environment")
			}
			return
		}
		for _, projected := range [][]string{env, complete} {
			for _, entry := range projected {
				key, _, _ := strings.Cut(entry, "=")
				if key == "API_TOKEN" || key == "SPARKWING_NODE_TOKEN" {
					t.Fatal("credential reached SDK or cleanup environment")
				}
			}
		}
		assignment := name + "=" + value
		row, _ := procenv.Lookup(name)
		if !slices.Contains(complete, assignment) || (startup || row.Startup) && !slices.Contains(env, assignment) {
			t.Fatal("cleanup or startup lost its persisted assignment")
		}
	})
}
