package store

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestAdmissionSourceLocationRequiresServerVerification(t *testing.T) {
	for _, mode := range []runspec.IntakeMode{runspec.IntakeExecute, runspec.IntakePublish} {
		t.Run(string(mode), func(t *testing.T) {
			tenant, in, _ := intakeFixture(t)
			in.Request.Mode = mode
			in.Request.Source.Location = runspec.SourceLocation{RemoteURL: "https://unrelated.example/private", PipelineDir: "ci"}
			if _, err := tenant.AdmitRun(t.Context(), in); err == nil {
				t.Fatal("typed repository identity authorized an unrelated acquisition location")
			}
			rewriteSchemaCount(t, tenant.s.db, "runs", 0)
			rewriteSchemaCount(t, tenant.s.db, "triggers", 0)
		})
	}
}

func TestAdmissionAndRetryPreserveVerifiedAcquisitionLocation(t *testing.T) {
	tenant, in, _ := intakeFixture(t)
	want := runspec.SourceLocation{RemoteURL: "https://github.com/acme/repo.git", PipelineDir: "ci"}
	tenant.s.SetIntakePolicy(fakeIntakePolicy(func(ctx context.Context, resolver capability.AuthorityResolver, in IntakeRequest) (IntakePolicyFacts, error) {
		if _, err := resolver.ResolveAuthority(ctx, in.Authority); err != nil {
			return IntakePolicyFacts{}, err
		}
		return IntakePolicyFacts{SourceLocation: &want, LogRetentionClass: "r30", LogRetentionDays: 30}, nil
	}))
	in.Request.Source.Location = runspec.SourceLocation{LocalCheckout: "/untrusted", PipelineDir: "elsewhere"}
	admitted, err := tenant.AdmitRun(t.Context(), in)
	if err != nil || admitted.Request.Request.Source.Location != want {
		t.Fatalf("verified source: %+v, %v", admitted.Request.Request.Source.Location, err)
	}
	request := IntakeRequest{Authority: in.Authority, Request: runspec.Request{Lineage: runspec.Lineage{RetryOf: admitted.RunID}, Source: runspec.Source{Location: runspec.SourceLocation{RemoteURL: "https://changed.example/repo"}}}}
	retry, err := tenant.AdmitRun(t.Context(), request)
	if err != nil || retry.Request.Request.Source.Location != want || retry.Request.Request.Git.SHA != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("retry source drift: %+v, %v", retry.Request.Request.Source, err)
	}
}
