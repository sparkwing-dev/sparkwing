package controller

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestFloodRunTokenSurvivesPolicyRechecksProperty(t *testing.T) {
	property := func(rechecks uint8) bool {
		s := (&Server{logger: slog.Default()}).WithFloodPolicy(FloodPolicy{RunsPerPrincipalHour: 1})
		p := capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "principal"}}
		request := store.IntakeRequest{Authority: p.Authority}
		calls := 0
		admit := func(ctx context.Context) (controllerwire.CreateRunResponse, error) {
			calls++
			for range int(rechecks) + 1 {
				if _, err := s.admissionFloodRefusal(ctx, nil, request, p); err != nil {
					return controllerwire.CreateRunResponse{}, err
				}
			}
			other := p
			other.Authority.ID = "other"
			var pending *intakeRunToken
			_, err := s.admissionFloodRefusal(ctx, nil, request, other)
			if !errors.As(err, &pending) {
				return controllerwire.CreateRunResponse{}, errors.New("run token authorized another principal")
			}
			return controllerwire.CreateRunResponse{RunID: "admitted"}, nil
		}
		result, err := admitWithFlood(t.Context(), s, admit)
		if err != nil || result.RunID != "admitted" || calls != 2 {
			return false
		}
		_, err = admitWithFlood(t.Context(), s, admit)
		var refusal *apierr.Error
		return errors.As(err, &refusal) && refusal.Code == apierr.RateLimited && calls == 3
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal("policy rechecks must spend one token, scoped to the requesting principal:", err)
	}
}
