package nodeproto

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func FuzzSecretIdentifierBoundary(f *testing.F) {
	for field := range uint8(26) {
		for placement := range uint8(6) {
			for _, expansion := range []bool{false, true} {
				f.Add("42", field, placement, expansion)
			}
		}
	}
	for i, secret := range []string{"sëcret", "jobs", "!", "\"\\\n", "a\x00b", "重复", "prefix-tail", "\xff"} {
		f.Add(secret, uint8(i), uint8(i%6), i%2 == 0)
	}
	f.Fuzz(func(t *testing.T, secret string, field, placement uint8, expansion bool) {
		secret = strings.ToValidUTF8(secret, "\ufffd")
		if secret == "" {
			return
		}
		identifier := "prefix-" + secret + "-suffix"
		job := secretIdentifierJob(identifier, field)
		placements := [...]Placement{PlacementLocal, PlacementLocalTriggered, PlacementRunnerHosted, PlacementLauncher, PlacementHere, PlacementGitHubActions}
		ctx := ValidationContext{Placement: placements[placement%6], Secrets: planSecrets{secret, ""}, Expansion: expansion}
		if expansion {
			ctx.SourceJob = "source"
			ctx.ExistingJobs = []JobSpec{{ID: "source", Steps: []StepSpec{{ID: "run"}}, Closures: ClosureSpec{Generate: []string{"fanout"}}}}
			job.Groups = append(job.Groups, "fanout")
		}
		raw, err := json.Marshal(PlanDoc{Jobs: []JobSpec{job}})
		if err != nil {
			t.Fatal(err)
		}
		plan, err := ValidatePlan(raw, ctx)
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.SecretInIdentifier || plan.Jobs != nil {
			t.Fatalf("secret-bearing identifier accepted or misclassified: %+v, %v", plan, err)
		}
		diagnostics := []string{failure.Message, failure.Hint}
		for _, value := range failure.Details {
			diagnostics = append(diagnostics, fmt.Sprint(value))
		}
		for _, text := range diagnostics {
			if strings.Contains(text, secret) || strings.Contains(text, identifier) {
				t.Fatalf("identifier diagnostic exposed a registered value: %q", text)
			}
		}
	})
}

func secretIdentifierJob(identifier string, field uint8) JobSpec {
	job := JobSpec{ID: "job", Steps: []StepSpec{{ID: "run"}}}
	switch field % 26 {
	case 0:
		job.ID = identifier
	case 1:
		job.Steps[0].ID = identifier
	case 2:
		job.Groups = []string{identifier}
	case 3:
		job.ID, job.Approval, job.Steps = identifier, &ApprovalSpec{}, nil
	case 4:
		job.Needs = []string{identifier}
	case 5:
		job.NeedsIfPresent = []string{identifier}
	case 6:
		job.NeedsGenerated = []string{identifier}
	case 7:
		job.Steps[0].Needs = []string{identifier}
	case 8:
		job.Steps[0].Groups = []string{identifier}
	case 9:
		job.ResultStep = identifier
	case 10:
		job.OnFailureOf = identifier
	case 11:
		job.Consumes = []ArtifactInput{{Producer: identifier}}
	case 12:
		job.References = []PipelineReference{{Pipeline: identifier, Job: "result"}}
	case 13:
		job.References = []PipelineReference{{Pipeline: "pipeline", Job: identifier}}
	case 14:
		job.Concurrency = []ConcurrencySpec{{Group: identifier}}
	case 15:
		job.Closures.Generate = []string{identifier}
	case 16:
		job.Closures.SkipIf = []string{identifier}
	case 17:
		job.Closures.BeforeRun = []string{identifier}
	case 18:
		job.Closures.AfterRun = []string{identifier}
	case 19:
		job.Closures.Verify = identifier
	case 20:
		job.Closures.MemoKey, job.Memo = identifier, &MemoSpec{}
	case 21:
		job.Requires = []string{identifier}
	case 22:
		job.Prefers = []string{identifier}
	case 23:
		job.Env = map[string]EnvValue{identifier: {Value: "body"}}
	case 24:
		job.Caches = []CacheSpec{{Name: identifier, KeyFiles: []string{}}}
	case 25:
		job.Artifacts = []string{identifier}
	}
	return job
}
