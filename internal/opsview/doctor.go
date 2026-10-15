package opsview

import (
	"sort"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/installsite"
)

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

type DoctorInstallConflict struct {
	Self string `json:"self"`

	Competing []DoctorInstallCopy `json:"competing"`
}

type DoctorInstallCopy struct {
	Path     string `json:"path"`
	Resolved string `json:"resolved,omitempty"`

	Modified string `json:"modified,omitempty"`
}

func InstallConflict(self string, dirs []string) *DoctorInstallConflict {
	competing := installsite.Competing(installsite.Scan(dirs), self)
	if len(competing) == 0 {
		return nil
	}
	sort.Slice(competing, func(i, j int) bool {
		return competing[i].ModTime.After(competing[j].ModTime)
	})
	out := &DoctorInstallConflict{Self: self}
	for _, c := range competing {
		row := DoctorInstallCopy{Path: c.Path, Modified: c.ModTime.UTC().Format(time.RFC3339)}
		if c.Resolved != c.Path {
			row.Resolved = c.Resolved
		}
		out.Competing = append(out.Competing, row)
	}
	return out
}
