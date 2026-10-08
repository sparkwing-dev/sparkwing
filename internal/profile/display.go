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
	case backends.TypeS3:
		out := s.Type + "://" + s.Bucket
		if s.Prefix != "" {
			out += "/" + s.Prefix
		}
		return out
	case backends.TypeFilesystem:
		return "filesystem:" + s.Path
	case backends.TypeController:
		return "controller://" + s.Controller
	case backends.TypePostgres:
		if s.URLSource != "" {
			return s.Type + ":" + s.URLSource
		}
		return s.Type
	case backends.TypeStdout:
		return "stdout"
	default:
		return s.Type
	}
}

func (p *Profile) SurfaceStrings() (state, logs, cache string) {
	if p == nil {
		return "-", "-", "-"
	}
	surf := p.Surfaces()
	if surf.State == nil && surf.Cache == nil && surf.Logs == nil && p.ControllerURL() != "" {
		c := "controller://" + p.Name
		return c, c, c
	}
	state = SpecString(surf.State)
	if surf.State == nil && p.ControllerURL() == "" {
		state = "sqlite"
	}
	return state, SpecString(surf.Logs), SpecString(surf.Cache)
}
