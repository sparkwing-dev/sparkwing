package localcontroller

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// NewScheduleService binds local scheduling to the elected controller's store and sealed-intake launcher.
func NewScheduleService(binding Binding, version string, launcher crons.Launcher) (*crons.Service, error) {
	if binding.Store == nil || binding.Tenant == nil || binding.Tenant.Team() != store.DefaultTeam || binding.Controller == nil || binding.Controller.Store != binding.Store || !filepath.IsAbs(binding.Home) || version == "" || launcher == nil {
		return nil, errors.New("local schedules require the elected default-team store, home, version and intake launcher")
	}
	subject, err := wingd.CurrentLocalSubject()
	if err != nil {
		return nil, err
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	return &crons.Service{
		Store: binding.Store, Schedules: binding.Tenant, Launcher: launcher,
		Host: host, Version: version, ArmedBy: subject, Side: store.CronWhereLocal,
		LockPath: filepath.Join(binding.Home, "crons.lock"), PinRoot: filepath.Join(binding.Home, "crons"),
	}, nil
}
