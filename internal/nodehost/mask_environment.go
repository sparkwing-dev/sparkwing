package nodehost

import (
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func (r *MaskRegistry) environment(build func(procenv.Kind, procenv.Base, procenv.Runtime) (procenv.Env, error)) func(procenv.Kind, procenv.Base, procenv.Runtime) (procenv.Env, error) {
	if build == nil {
		build = procenv.Build
	}
	return func(kind procenv.Kind, base procenv.Base, runtime procenv.Runtime) (procenv.Env, error) {
		candidate := secrets.NewMasker()
		base.Masker = candidate
		environment, err := build(kind, base, runtime)
		if err != nil {
			return procenv.Env{}, err
		}
		if err := r.register(candidate); err != nil {
			return procenv.Env{}, err
		}
		return environment, nil
	}
}
