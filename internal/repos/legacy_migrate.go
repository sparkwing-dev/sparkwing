package repos

import (
	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

// hack: temporary, and deleted with userconfig's legacy migration.
func init() {
	userconfig.RegisterLegacyValidator(userconfig.Repos, func(section *yaml.Node) error {
		var cfg Config
		return userconfig.DecodeStrict(section, &cfg)
	})
}
