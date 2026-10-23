package profile

import (
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func specString(s *backends.Spec) string {
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
