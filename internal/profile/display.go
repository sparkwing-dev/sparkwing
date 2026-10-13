package profile

import (
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func SpecString(s *backends.Spec) string {
	if s == nil {
		return "-"
	}
	switch s.Type {
	case backends.TypeSQLite:
		if s.Path != "" {
			return "sqlite:" + s.Path
		}
		return "sqlite"
	case backends.TypeFilesystem:
		return "filesystem:" + s.Path
	case backends.TypeController:
		return "controller://" + s.Controller
	default:
		return s.Type
	}
}

// StateString names where p's run records live.
func (p *Profile) StateString() string {
	switch {
	case p == nil:
		return "-"
	case p.State != nil:
		return SpecString(p.State)
	case p.ControllerURL() != "":
		return "controller://" + p.Name
	default:
		return "sqlite"
	}
}
