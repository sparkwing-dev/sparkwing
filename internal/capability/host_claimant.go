package capability

import "slices"

// HostSubject names a self-hosted controller's own claimant.
const HostSubject = "controller-host"

// HostClaimant reports whether c is a self-hosted controller's own claim root.
// Service enrollment cannot grant triggers.claim, so no API caller can mint one.
func HostClaimant(c Capability) bool {
	return c.Subject == Subject{Kind: SubjectService, ID: HostSubject} && c.Team == "" && c.Parent == nil && c.Unit == "" && c.Binding.Kind == BindingNone &&
		slices.Contains(c.Grants, TriggersClaim) && !slices.Contains(c.Grants, ClaimsLaunch)
}
