package nodehost

import (
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func (r *MaskRegistry) environment(build func(procenv.Kind, procenv.Base, procenv.Runtime) ([]string, error)) func(procenv.Kind, procenv.Base, procenv.Runtime) ([]string, error) {
	if build == nil {
		build = procenv.Build
	}
	return func(kind procenv.Kind, base procenv.Base, runtime procenv.Runtime) ([]string, error) {
		candidate := secrets.NewMasker()
		base.Masker = candidate
		environment, err := build(kind, base, runtime)
		if err != nil {
			return nil, err
		}
		if err := r.register(candidate); err != nil {
			return nil, err
		}
		return environment, nil
	}
}
