package controllerwire

// WingStatus is the Wing status document for one authorized team.
type WingStatus struct {
	Source    string           `json:"source"`
	State     string           `json:"state"`
	Summary   string           `json:"summary"`
	URL       string           `json:"url"`
	UpdatedAt string           `json:"updatedAt"`
	Items     []WingStatusItem `json:"items"`
}

// WingStatusItem reports one pipeline or operational check.
type WingStatusItem struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	Detail    string `json:"detail"`
	URL       string `json:"url"`
	UpdatedAt string `json:"updatedAt"`
}
