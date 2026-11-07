package wingd

import "github.com/sparkwing-dev/sparkwing/internal/userconfig"

// DefaultDashboardAddress is the stable loopback address bookmarks and HTTPS proxies name.
const DefaultDashboardAddress = "127.0.0.1:4343"

// DashboardSettings is the dashboard a shared local controller binds when it starts.
type DashboardSettings struct {
	Address  string
	ReadOnly bool
}

// ResolveDashboardSettings reads the dashboard section of the machine configuration.
func ResolveDashboardSettings() (DashboardSettings, error) {
	var raw userconfig.DashboardConfig
	if _, _, err := userconfig.ReadDefault(userconfig.Dashboard, &raw); err != nil {
		return DashboardSettings{}, err
	}
	settings := DashboardSettings{Address: DefaultDashboardAddress}
	if raw.Address != nil {
		settings.Address = *raw.Address
	}
	if raw.ReadOnly != nil {
		settings.ReadOnly = *raw.ReadOnly
	}
	return settings, nil
}
