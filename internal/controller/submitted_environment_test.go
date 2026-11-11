package controller

import (
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestCreateRunNamesRefusedSubmittedVariable(t *testing.T) {
	for _, name := range []string{"APPDATA", "PSModulePath"} {
		t.Run(name, func(t *testing.T) {
			_, options, input, _ := sourceUploadFixture(t)
			const value = "private-requester-value-3483"
			input.EnvironmentSource = runspec.EnvironmentSubmitted
			var err error
			input.Env, err = runspec.EnvironmentDigest(map[string]string{name: value}, strings.Repeat("5", 64))
			if err != nil {
				t.Fatal(err)
			}
			input.Options.SubmitEnvAllow = runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{name}}}
			c, err := client.NewEngineClient(options)
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.CreateRun(t.Context(), controllerwire.CreateRunRequest{Request: input, IdempotencyKey: "reserved", SubmittedEnv: map[string][]byte{name: []byte(value)}})
			var public *apierr.Error
			if !errors.As(err, &public) || public.Code != apierr.BadRequest {
				t.Fatalf("refusal class: %v", err)
			}
			for _, want := range []string{name, "job environment", "runner host", "run.submit_env_allow"} {
				if !strings.Contains(public.Message, want) {
					t.Errorf("refusal lacks %q: %+v", want, public)
				}
			}
			if strings.Contains(public.Message+public.Hint, value) {
				t.Fatal("refusal exposes private value")
			}
		})
	}
}
