package wingd

import (
	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

// hack: temporary, and deleted with userconfig's legacy migration.
func init() {
	userconfig.RegisterLegacyValidator(userconfig.Admission, func(section *yaml.Node) error {
		var raw admissionConfigFile
		if err := userconfig.DecodeStrict(section, &raw); err != nil {
			return err
		}
		if _, err := admissionPolicyFrom(raw); err != nil {
			return err
		}
		_, err := ParseBudget(raw.Budget)
		return err
	})
}
