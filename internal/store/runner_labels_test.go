package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

//sparkwing:critical
func TestRunnerLabelsBoundsApplyToEnrollmentAdminAndPoll(t *testing.T) {
	s, spec := machineEnrollment(t)
	for name, labels := range map[string][]string{
		"count":     strings.Fields(strings.Repeat("gpu ", 33)),
		"length":    {strings.Repeat("a", 129)},
		"character": {"gpu\nprod"},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := spec
			invalid.Subject.ID = "bad-" + name
			invalid.RegistrationID = "runner:a:" + invalid.Subject.ID
			invalid.AllowedLabels = labels
			_, err := s.AsOperator().ProvisionCapability(t.Context(), invalid)
			var bad *apierr.Error
			if !errors.As(err, &bad) || bad.Code != apierr.BadRequest {
				t.Fatalf("invalid enrollment accepted: %v", err)
			}
		})
	}
	spec.AllowedLabels = nil
	for i := range 32 {
		spec.AllowedLabels = append(spec.AllowedLabels, fmt.Sprintf("gpu-%d", i))
	}
	spec.AllowedLabels[0] = strings.Repeat("a", 128)
	issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := s.AsOperator().ResolveAuthority(t.Context(), *spec.Authority)
	if err != nil {
		t.Fatal(err)
	}
	for _, labels := range [][]string{{strings.Repeat("x", 129)}, {"gpu;prod"}, strings.Fields(strings.Repeat("gpu ", 33))} {
		var bad *apierr.Error
		if _, err := s.AsOperator().ConfigureRunnerLabels(t.Context(), "a", spec.Subject.ID, admin.Capability.Subject.ID, &labels); !errors.As(err, &bad) || bad.Code != apierr.BadRequest {
			t.Fatalf("invalid admin update accepted: %v", err)
		}
		if err := s.AsOperator().RecordRunnerPoll(t.Context(), UnitDemand{CapabilityPrefix: issued.Capability.Prefix, Labels: labels}); !errors.As(err, &bad) || bad.Code != apierr.BadRequest {
			t.Fatalf("invalid poll accepted: %v", err)
		}
	}
	got, err := s.AsOperator().ConfigureRunnerLabels(t.Context(), "a", spec.Subject.ID, admin.Capability.Subject.ID, nil)
	if err != nil || !slices.Equal(got, spec.AllowedLabels) {
		t.Fatalf("invalid updates changed allowed labels: %v %v", got, err)
	}
}
