package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

// VerifiedRunDelivery contains facts loaded from a verified App delivery, never a request body.
type VerifiedRunDelivery struct {
	Git         runspec.Git
	Actor       runspec.Actor
	PullRequest runspec.Optional[runspec.PullRequest]
}

// IntakePolicyFacts captures server policy while the admission transaction holds live authority.
type IntakePolicyFacts struct {
	Options              *runspec.Options
	Ceilings             runspec.Ceilings
	RepositoryAuthorized bool
	SourceLocation       *runspec.SourceLocation
	Delivery             *VerifiedRunDelivery
	LogRetentionClass    string
	LogRetentionDays     int
}

// IntakePolicy resolves request options against operator ceilings, retention and verified repository facts.
// Implementations use C4 for options and the supplied resolver for live authority; input origins grant no trust.
type IntakePolicy interface {
	ResolveIntake(context.Context, capability.AuthorityResolver, IntakeRequest) (IntakePolicyFacts, error)
}

// SetIntakePolicy configures the server policy lookup before the store is shared.
// An unconfigured store refuses intake rather than accepting missing operator ceilings.
func (s *Store) SetIntakePolicy(policy IntakePolicy) { s.intakePolicy = policy }

type intakeContracts interface {
	SealRequest(runspec.Request, runspec.IntakeFacts) (runspec.SealedRequest, error)
	SealSpec(runspec.SealInput) (runspec.Spec, error)
	CarryRetry(runspec.RetryInput) (runspec.RetrySnapshot, error)
	ResolvePlan(settings.PlanInputs) (runspec.ExecutionSettings, error)
}

type sharedIntakeContracts struct{}

func (sharedIntakeContracts) SealRequest(request runspec.Request, facts runspec.IntakeFacts) (runspec.SealedRequest, error) {
	return runspec.SealRequest(request, facts)
}

func (sharedIntakeContracts) SealSpec(input runspec.SealInput) (runspec.Spec, error) {
	return runspec.SealSpec(input)
}

func (sharedIntakeContracts) CarryRetry(input runspec.RetryInput) (runspec.RetrySnapshot, error) {
	return runspec.CarryRetry(input)
}

func (sharedIntakeContracts) ResolvePlan(input settings.PlanInputs) (runspec.ExecutionSettings, error) {
	return settings.ResolvePlan(input)
}

func (s *Store) intakeServices() intakeContracts {
	if s.intake != nil {
		return s.intake
	}
	return sharedIntakeContracts{}
}
