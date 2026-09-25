package profile

import (
	"fmt"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

// hack: temporary, and deleted with userconfig's legacy migration.
func init() {
	userconfig.RegisterLegacyValidator(userconfig.Profiles, func(section *yaml.Node) error {
		var profiles map[string]*Profile
		if err := userconfig.DecodeStrict(section, &profiles); err != nil {
			return err
		}
		for name, p := range profiles {
			if p == nil {
				continue
			}
			p.Name = name
			p.InheritControllerDefaults()
			if err := p.validateSurfaceFields(); err != nil {
				return fmt.Errorf("profile %q: %w", name, err)
			}
		}
		return nil
	})
}
