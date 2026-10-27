package controllerwire

// TeamSettings governs machine enrollment, claim ranking, child-run limits and
// cache trust. An empty ClaimRanking means first come. HighTrust, off by
// default, lets verified runs on plain-machine and persistent runners write
// the shared verified cache that fresh pods and Cloud restore.
type TeamSettings struct {
	MembersCanAddMachines bool     `json:"members_can_add_machines"`
	ClaimRanking          []string `json:"claim_ranking"`
	MaxChildrenPerRoot    int64    `json:"max_children_per_root"`
	HighTrust             bool     `json:"high_trust"`
}

// TeamSettingsUpdate changes only the settings it names. A ClaimRanking of ["default"] restores the default ranking.
type TeamSettingsUpdate struct {
	MembersCanAddMachines *bool     `json:"members_can_add_machines,omitempty"`
	ClaimRanking          *[]string `json:"claim_ranking,omitempty"`
	MaxChildrenPerRoot    *int64    `json:"max_children_per_root,omitempty"`
	HighTrust             *bool     `json:"high_trust,omitempty"`
}
