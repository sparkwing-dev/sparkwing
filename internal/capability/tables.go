// Policy tables are unexported, and every accessor, LookupRoute included,
// hands out a copy that shares no slice with the table, so reading policy
// cannot change authorization.
package capability

import "slices"

// copied returns table's elements, each passed through deep when the element
// type holds slices.
func copied[T any](table []T, deep func(T) T) []T {
	out := slices.Clone(table)
	if deep != nil {
		for i := range out {
			out[i] = deep(out[i])
		}
	}
	return out
}

func (p RoutePolicy) clone() RoutePolicy {
	p.Access = slices.Clone(p.Access)
	for i := range p.Access {
		p.Access[i].Grants = slices.Clone(p.Access[i].Grants)
	}
	return p
}

func (p AwardPolicy) clone() AwardPolicy {
	p.ClaimGrants = slices.Clone(p.ClaimGrants)
	p.Grants = slices.Clone(p.Grants)
	p.Bindings = slices.Clone(p.Bindings)
	return p
}

// LivenessPolicies returns a copy of the liveness policies table.
func LivenessPolicies() []LivenessPolicy { return copied(livenessPolicies, nil) }

// PaymentEventPolicies returns a copy of the payment event policies table.
func PaymentEventPolicies() []PaymentEventPolicy { return copied(paymentEventPolicies, nil) }

// CredentialPrefixes returns a copy of the credential prefixes table.
func CredentialPrefixes() []string { return copied(credentialPrefixes, nil) }

// DataPolicies returns a copy of the data policies table.
func DataPolicies() []DataPolicy { return copied(dataPolicies, nil) }

// UnitBillingMappings returns a copy of the unit billing mappings table.
func UnitBillingMappings() []UnitBilling { return copied(unitBillingMappings, nil) }

// BillingRules returns a copy of the billing rules table.
func BillingRules() []BillingRule { return copied(billingRules, nil) }

// Grants returns a copy of the grants table.
func Grants() []Grant { return copied(grants, nil) }

// BootstrapGrants returns a copy of the bootstrap grants table.
func BootstrapGrants() []Grant { return copied(bootstrapGrants, nil) }

// BootstrapUnits returns a copy of the bootstrap units table.
func BootstrapUnits() []UnitKind { return copied(bootstrapUnits, nil) }

// AwardPolicies returns a copy of the award policies table.
func AwardPolicies() []AwardPolicy { return copied(awardPolicies, AwardPolicy.clone) }

// Routes returns a copy of the routes table.
func Routes() []RoutePolicy { return copied(routes, RoutePolicy.clone) }

// LookupRoute accepts the router's registered pattern, never a prefix match on untrusted paths.
func LookupRoute(method, pattern string) (RoutePolicy, bool) {
	for _, route := range routes {
		if route.Method == method && route.Path == pattern {
			return route.clone(), true
		}
	}
	return RoutePolicy{}, false
}
