package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestAdmissionRefusesRemoteWorkingTreeBeforeQueueing(t *testing.T) {
	for _, bundle := range []bool{false, true} {
		t.Run(map[bool]string{false: "remote", true: "uploaded snapshot"}[bundle], func(t *testing.T) {
			tenant, in, _ := intakeFixture(t)
			location := runspec.SourceLocation{RemoteURL: "https://github.com/acme/repo.git", PipelineDir: ".sparkwing"}
			tenant.s.intakePolicy = fakeIntakePolicy(func(context.Context, capability.AuthorityResolver, IntakeRequest) (IntakePolicyFacts, error) {
				return IntakePolicyFacts{SourceLocation: &location, LogRetentionClass: "r30", LogRetentionDays: 30}, nil
			})
			in.Request.Dispatch = ""
			in.Request.Source.WorkingTree = true
			in.Request.Source.BundleDigest = runspec.Optional[string]{Set: bundle, Value: strings.Repeat("a", 64)}
			_, err := tenant.AdmitRun(t.Context(), in)
			var refused *apierr.Error
			if !errors.As(err, &refused) || refused.Code != apierr.InvalidArgs || !strings.Contains(refused.Message, "local checkout") || !strings.Contains(refused.Message, "pipeline trigger --working-tree") {
				t.Fatalf("remote working tree refusal = %v", err)
			}
			rewriteSchemaCount(t, tenant.s.db, "runs", 0)
			rewriteSchemaCount(t, tenant.s.db, "triggers", 0)
		})
	}
}

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
	tenant.s.intakePolicy = fakeIntakePolicy(func(ctx context.Context, resolver capability.AuthorityResolver, in IntakeRequest) (IntakePolicyFacts, error) {
		if _, err := resolver.ResolveAuthority(ctx, in.Authority); err != nil {
			return IntakePolicyFacts{}, err
		}
		return IntakePolicyFacts{SourceLocation: &want, LogRetentionClass: "r30", LogRetentionDays: 30}, nil
	})
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
