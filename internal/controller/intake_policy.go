package controller

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

// ResolveIntake resolves captured preferences and trusted deployment policy before sealing.
func (s *Server) ResolveIntake(ctx context.Context, resolver capability.AuthorityResolver, request store.IntakeRequest) (store.IntakePolicyFacts, error) {
	principal, err := resolver.ResolveAuthority(ctx, request.Authority)
	if err != nil {
		return store.IntakePolicyFacts{}, err
	}
	admissionKey, err := s.admissionFloodRefusal(ctx, resolver, request, principal)
	if err != nil {
		return store.IntakePolicyFacts{}, err
	}
	team := store.Team(principal.Capability.Team)
	guards, funded, err := store.IntakeSettings(ctx, resolver, team)
	if err != nil {
		return store.IntakePolicyFacts{}, err
	}
	operator := settings.OperatorInputs{}
	if guards.RunSeconds > 0 {
		if guards.RunSeconds > math.MaxInt64/int64(time.Second) {
			return store.IntakePolicyFacts{}, &apierr.Error{Code: apierr.Unavailable, Message: "operator run duration is not representable"}
		}
		duration := time.Duration(guards.RunSeconds) * time.Second
		operator.Global = settings.OperatorValues{OwnerExecution: &duration, Origins: map[string]settings.Origin{"operator.owner_execution": {Source: settings.GlobalDB, Key: store.ComputeLimitRunSeconds}}}
	}
	values, err := settings.ControllerCeilings(operator)
	if err != nil {
		return store.IntakePolicyFacts{}, err
	}
	ceilings, err := settings.ProjectCeilings(values)
	if err != nil {
		return store.IntakePolicyFacts{}, err
	}
	localPeer := principal.Capability.Subject.Kind == capability.SubjectLocal
	published := request.Request.Mode == runspec.IntakePublish
	if !localPeer && !published && request.Request.Origin == runspec.OriginLocal {
		return store.IntakePolicyFacts{}, &apierr.Error{Code: apierr.Forbidden, Message: "executable local origin requires local peer authority"}
	}
	placement := store.RequestPlacement(request.Request)
	options, err := settings.ResolveIntake(request.Request.Options, operator, settings.Context{Origin: request.Request.Origin, Local: placement == nodeproto.PlacementLocal, NumCPU: runtime.NumCPU()})
	if err != nil {
		return store.IntakePolicyFacts{}, err
	}
	days := int(limits.FreeLogRetention / (24 * time.Hour))
	if funded {
		days = int(limits.FundedLogRetention / (24 * time.Hour))
	}
	if override := s.logRetention[team]; override > 0 {
		days = override
	}
	facts := store.IntakePolicyFacts{AdmissionKey: admissionKey, Options: &options, Ceilings: ceilings, LogRetentionClass: fmt.Sprintf("r%d", days), LogRetentionDays: days}
	if request.Authority.Kind == "schedule" {
		location, err := store.VerifiedScheduleIntake(ctx, resolver, request)
		facts.SourceLocation = &location
		return facts, err
	}
	if request.VerifiedDeliveryID != "" {
		delivery, err := store.VerifiedAppIntake(ctx, resolver, request)
		if err != nil {
			return facts, err
		}
		facts.Delivery = &delivery
		location := request.Request.Source.Location
		facts.SourceLocation = &location
		return facts, nil
	}
	location := request.Request.Source.Location
	if strings.HasPrefix(string(request.Request.Git.Repository.Key), "gh:") && !localPeer {
		verified, ok := ctx.Value(verifiedIntakeSourceKey{}).(verifiedIntakeSource)
		if !ok || verified.location != location || verified.repository != request.Request.Git.Repository {
			return facts, &apierr.Error{Code: apierr.Forbidden, Message: "GitHub source requires verified intake repository identity"}
		}
	}
	if location == (runspec.SourceLocation{}) {
		return facts, nil
	}
	if location.PipelineDir != "" && !filepath.IsLocal(location.PipelineDir) {
		return facts, &apierr.Error{Code: apierr.BadRequest, Message: "pipeline directory must stay within the checkout"}
	}
	if location.RemoteURL != "" {
		if _, err := parseIntakeRemote(location.RemoteURL); err != nil {
			return facts, err
		}
	}
	if localPeer {
		facts.SourceLocation = &location
		return facts, nil
	}
	if location.LocalCheckout != "" {
		return facts, &apierr.Error{Code: apierr.Forbidden, Message: "remote intake cannot name a local checkout"}
	}
	if !strings.HasPrefix(string(request.Request.Git.Repository.Key), "gh:") {
		repository, err := runspec.CanonicalRepository(runspec.RepoInput{RemoteURL: location.RemoteURL})
		if err != nil || repository.Key != request.Request.Git.Repository.Key {
			return facts, &apierr.Error{Code: apierr.Forbidden, Message: "source does not match repository identity"}
		}
	}
	facts.SourceLocation = &location
	return facts, nil
}
