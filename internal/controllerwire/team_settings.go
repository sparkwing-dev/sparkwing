package controllerwire

// TeamSettings governs member enrollment of personal machines and the run-anywhere claim ranking.
// An empty ClaimRanking means first come.
type TeamSettings struct {
	MembersCanAddMachines bool     `json:"members_can_add_machines"`
	ClaimRanking          []string `json:"claim_ranking"`
}

// TeamSettingsUpdate changes only the settings it names.
type TeamSettingsUpdate struct {
	MembersCanAddMachines *bool     `json:"members_can_add_machines,omitempty"`
	ClaimRanking          *[]string `json:"claim_ranking,omitempty"`
}

// MachineAutomation reports a personal machine's standing for its owner's automated runs: none, requested or approved.
type MachineAutomation struct {
	Runner string `json:"runner"`
	State  string `json:"state"`
}

// MachineAutomationRequest is the owner's request or withdrawal.
type MachineAutomationRequest struct {
	Requested *bool `json:"requested"`
}

// MachineAutomationApproval is a team administrator's approval or revocation.
type MachineAutomationApproval struct {
	Approved *bool `json:"approved"`
}
