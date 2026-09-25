package fleet

import (
	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

// hack: temporary, and deleted with userconfig's legacy migration.
func init() {
	userconfig.RegisterLegacyValidator(userconfig.Fleet, func(section *yaml.Node) error {
		cfg := Config{Local: Local{MaxConcurrent: 1, Contribution: "50%,50%"}}
		if err := userconfig.DecodeStrict(section, &cfg); err != nil {
			return err
		}
		return cfg.validate(LocalTailscaleIPs)
	})
}
