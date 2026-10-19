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

// MachineAutomation is a personal machine's opt-in to its owner's automated runs.
type MachineAutomation struct {
	Runner        string `json:"runner"`
	AutomatedRuns bool   `json:"automated_runs"`
}
