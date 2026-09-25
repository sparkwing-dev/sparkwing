package agentconfig

import (
	"errors"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

// hack: temporary, and deleted with userconfig's legacy migration.
func init() {
	userconfig.RegisterLegacyValidator(userconfig.Agent, func(section *yaml.Node) error {
		if carriesEnrolledKey(section, 0) {
			return errors.New(EnrolledModeRemoved)
		}
		var cfg Config
		if err := userconfig.DecodeStrict(section, &cfg); err != nil {
			return err
		}
		_, err := Validate(cfg)
		return err
	})
}
