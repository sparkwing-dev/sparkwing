package controllerwire

// TeamSettings governs member enrollment of personal machines.
type TeamSettings struct {
	MembersCanAddMachines bool `json:"members_can_add_machines"`
}
