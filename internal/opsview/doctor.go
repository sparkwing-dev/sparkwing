package opsview

// DoctorGoToolchain describes the Go toolchain available to the current pipeline module.
type DoctorGoToolchain struct {
	Current  string `json:"current"`
	Setting  string `json:"gotoolchain"`
	Source   string `json:"source"`
	Floor    string `json:"floor"`
	Module   string `json:"module"`
	Override string `json:"override,omitempty"`
	Verdict  string `json:"verdict"`
	Error    string `json:"error,omitempty"`
}
