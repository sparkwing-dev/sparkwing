package profile

import (
	"os"
	"strings"

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
	case backends.TypeS3, backends.TypeGCS, backends.TypeAzureBlob:
		out := s.Type + "://" + s.Bucket
		if s.Prefix != "" {
			out += "/" + s.Prefix
		}
		return out
	case backends.TypeFilesystem:
		return "filesystem:" + s.Path
	case backends.TypeController:
		return "controller://" + s.Controller
	case backends.TypePostgres, backends.TypeMySQL:
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

func DisplayDefaultPath() string {
	path, err := DefaultPath()
	if err != nil || path == "" {
		return "profiles.yaml"
	}
	if home, herr := os.UserHomeDir(); herr == nil && home != "" {
		if rest, ok := strings.CutPrefix(path, home+"/"); ok {
			return "~/" + rest
		}
	}
	return path
}
