package nodehost

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestPlanningExplainsMultilineIdentifierMatch(t *testing.T) {
	for _, multiline := range []bool{false, true} {
		value := "production"
		if multiline {
			value = "header-canary\n  production  \nfooter-canary"
		}
		masker := secrets.NewMasker()
		if err := masker.Register(value); err != nil {
			t.Fatal(err)
		}
		merged := secrets.NewMasker()
		merged.Merge(masker)
		for name, matcher := range map[string]nodeproto.SecretMatcher{
			"direct": masker, "merged": merged, "scoped": merged.Scoped(),
			"planning": planningSecrets{prior: NewMaskRegistry(merged), masker: secrets.NewMasker()},
		} {
			for _, identifier := range []string{"deploy-production", "deploy-cHJvZHVjdGlvbg"} {
				t.Run(name+"/"+identifier+"/"+value[:1], func(t *testing.T) {
					raw, err := json.Marshal(nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: identifier}}})
					if err != nil {
						t.Fatal(err)
					}
					_, err = nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: matcher})
					assertIdentifierOrigin(t, err, multiline)
				})
			}
		}
	}
}

func TestMaskRegistryExplainsMultilineIdentifierMatch(t *testing.T) {
	for _, registerFirst := range []bool{false, true} {
		registry := NewMaskRegistry(secrets.NewMasker())
		plan := nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: "deploy-production"}}}
		var err error
		if registerFirst {
			if err = registry.registerValue("header-canary\nproduction\nfooter-canary"); err != nil {
				t.Fatal(err)
			}
			err = registry.acceptPlan(plan, func() error { return nil })
		} else {
			if err = registry.acceptPlan(plan, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			err = registry.registerValue("header-canary\nproduction\nfooter-canary")
		}
		assertIdentifierOrigin(t, err, true)
	}
}

func assertIdentifierOrigin(t *testing.T, err error, multiline bool) {
	t.Helper()
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != hosterr.SecretInIdentifier {
		t.Fatalf("identifier refusal: %v", err)
	}
	if strings.Contains(failure.Message, "line of a multi-line secret") != multiline {
		t.Fatalf("missing mask origin: %v", err)
	}
	if strings.Contains(err.Error(), "production") || strings.Contains(err.Error(), "cHJvZHVjdGlvbg") || !strings.Contains(failure.Message, "jobs[0].id") {
		t.Fatalf("unsafe identifier refusal: %v", err)
	}
}
