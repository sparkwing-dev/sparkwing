package orchestrator

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func TestProfileStateSpec(t *testing.T) {
	const home = "/tmp/state.db"
	for name, tc := range map[string]struct {
		p    *profile.Profile
		want backends.Spec
	}{
		"no profile":        {nil, backends.Spec{Type: backends.TypeSQLite, Path: home}},
		"controller only":   {&profile.Profile{Name: "prod", Controller: &profile.ControllerSpec{URL: "http://c.example"}}, backends.Spec{Type: backends.TypeController, Controller: "prod"}},
		"sqlite, no path":   {&profile.Profile{State: &backends.Spec{Type: backends.TypeSQLite}}, backends.Spec{Type: backends.TypeSQLite, Path: home}},
		"sqlite, own path":  {&profile.Profile{State: &backends.Spec{Type: backends.TypeSQLite, Path: "/x.db"}}, backends.Spec{Type: backends.TypeSQLite, Path: "/x.db"}},
		"explicit wins too": {&profile.Profile{Name: "prod", Controller: &profile.ControllerSpec{URL: "http://c.example"}, State: &backends.Spec{Type: backends.TypeSQLite, Path: "/x.db"}}, backends.Spec{Type: backends.TypeSQLite, Path: "/x.db"}},
	} {
		if got := profileStateSpec(tc.p, home); got == nil || *got != tc.want {
			t.Errorf("%s: spec = %#v, want %#v", name, got, tc.want)
		}
	}
}
