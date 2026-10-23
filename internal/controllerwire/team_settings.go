package controllerwire

// MaxChildrenPerRoot is the largest supported team descendant ceiling.
const MaxChildrenPerRoot int64 = 1000

// TeamSettings governs machine enrollment, claim ranking and child-run limits.
// An empty ClaimRanking means first come.
type TeamSettings struct {
	MembersCanAddMachines bool     `json:"members_can_add_machines"`
	ClaimRanking          []string `json:"claim_ranking"`
	MaxChildrenPerRoot    int64    `json:"max_children_per_root"`
}

// TeamSettingsUpdate changes only the settings it names. A ClaimRanking of ["default"] restores the default ranking.
type TeamSettingsUpdate struct {
	MembersCanAddMachines *bool     `json:"members_can_add_machines,omitempty"`
	ClaimRanking          *[]string `json:"claim_ranking,omitempty"`
	MaxChildrenPerRoot    *int64    `json:"max_children_per_root,omitempty"`
}
