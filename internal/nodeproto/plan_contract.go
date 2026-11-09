package nodeproto

import (
	"encoding/json"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

// CacheResolvers is every resolver a host accepts; the protocol schema snapshot
// lists the same values for SDKs written in other languages.
var CacheResolvers = []string{nodewire.CacheResolverGoModules, nodewire.CacheResolverNpm}

// SecretMatcher avoids copying secret values into validation errors or persisted plans.
type SecretMatcher interface{ ContainsSecret(identifier string) bool }

// ValidationContext supplies host facts without admitting those fields in SDK JSON.
type ValidationContext struct {
	Placement           Placement
	Secrets             SecretMatcher
	Expansion           bool
	ExistingJobs        []nodewire.JobSpec
	ExistingConcurrency []nodewire.ConcurrencySpec
	SourceJob           string
}

// ValidatePlan strictly decodes plans and expansions, rejecting invalid graphs and secret-bearing identifiers.
func ValidatePlan(raw json.RawMessage, context ValidationContext) (nodewire.PlanDoc, error) {
	plan, err := validatePlan(raw, context)
	return plan, sanitizePlanError(err, context.Secrets)
}
