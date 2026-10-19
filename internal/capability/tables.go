package capability

import "slices"

// LivenessPolicies returns a copy of the liveness policies table.
func LivenessPolicies() []LivenessPolicy { return slices.Clone(livenessPolicies) }

// PaymentEventPolicies returns a copy of the payment event policies table.
func PaymentEventPolicies() []PaymentEventPolicy { return slices.Clone(paymentEventPolicies) }

// CredentialPrefixes returns a copy of the credential prefixes table.
func CredentialPrefixes() []string { return slices.Clone(credentialPrefixes) }

// DataPolicies returns a copy of the data policies table.
func DataPolicies() []DataPolicy { return slices.Clone(dataPolicies) }

// UnitBillingMappings returns a copy of the unit billing mappings table.
func UnitBillingMappings() []UnitBilling { return slices.Clone(unitBillingMappings) }

// BillingRules returns a copy of the billing rules table.
func BillingRules() []BillingRule { return slices.Clone(billingRules) }

// Grants returns a copy of the grants table.
func Grants() []Grant { return slices.Clone(grants) }

// BootstrapGrants returns a copy of the bootstrap grants table.
func BootstrapGrants() []Grant { return slices.Clone(bootstrapGrants) }

// BootstrapUnits returns a copy of the bootstrap units table.
func BootstrapUnits() []UnitKind { return slices.Clone(bootstrapUnits) }

// AwardPolicies returns a deep copy of the award policies table.
func AwardPolicies() []AwardPolicy {
	out := slices.Clone(awardPolicies)
	for i := range out {
		out[i].ClaimGrants = slices.Clone(out[i].ClaimGrants)
		out[i].Grants = slices.Clone(out[i].Grants)
		out[i].Bindings = slices.Clone(out[i].Bindings)
	}
	return out
}

// Routes returns a deep copy of the routes table.
func Routes() []RoutePolicy {
	out := slices.Clone(routes)
	for i := range out {
		out[i].Access = slices.Clone(out[i].Access)
		for j := range out[i].Access {
			out[i].Access[j].Grants = slices.Clone(out[i].Access[j].Grants)
		}
	}
	return out
}
