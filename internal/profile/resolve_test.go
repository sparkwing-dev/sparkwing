package profile_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func cfgWith(profiles map[string]*profile.Profile, def string) *profile.Config {
	return &profile.Config{Default: def, Profiles: profiles}
}

func TestResolveChain_FlagWins(t *testing.T) {
	cfg := cfgWith(map[string]*profile.Profile{
		"prod": {Name: "prod"},
		"team": {Name: "team"},
		"dev":  {Name: "dev"},
	}, "dev")
	p, chain, err := profile.Resolve("prod", "team", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "prod" || chain.Selected != "prod" || chain.Source != profile.ChainSourceFlag {
		t.Fatalf("selected %q via %q, want prod via flag", chain.Selected, chain.Source)
	}
	// The lower rules that would have applied appear as Considered.
	if !consideredHas(chain, profile.ChainSourceProject, "team") {
		t.Errorf("project hint should appear as Considered: %+v", chain.Considered)
	}
	if !consideredHas(chain, profile.ChainSourceDefault, "dev") {
		t.Errorf("default should appear as Considered: %+v", chain.Considered)
	}
}

func TestResolveChain_ProjectHintWins(t *testing.T) {
	cfg := cfgWith(map[string]*profile.Profile{
		"team": {Name: "team"},
		"dev":  {Name: "dev"},
	}, "dev")
	_, chain, err := profile.Resolve("", "team", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if chain.Selected != "team" || chain.Source != profile.ChainSourceProject {
		t.Fatalf("selected %q via %q, want team via project", chain.Selected, chain.Source)
	}
}

func TestResolveChain_DefaultWins(t *testing.T) {
	cfg := cfgWith(map[string]*profile.Profile{
		"dev": {Name: "dev"},
	}, "dev")
	_, chain, err := profile.Resolve("", "", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if chain.Selected != "dev" || chain.Source != profile.ChainSourceDefault {
		t.Fatalf("selected %q via %q, want dev via default", chain.Selected, chain.Source)
	}
}

func TestResolveChain_BuiltinLaptopFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *profile.Config
	}{
		{"nil config", nil},
		{"empty config", cfgWith(map[string]*profile.Profile{}, "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, chain, err := profile.Resolve("", "", tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if chain.Selected != "laptop" || chain.Source != profile.ChainSourceBuiltin {
				t.Fatalf("selected %q via %q, want laptop via builtin", chain.Selected, chain.Source)
			}
			if p == nil || p.Name != "laptop" {
				t.Fatalf("profile = %#v, want synthetic laptop", p)
			}
		})
	}
}

func TestResolveChain_FlagNotFound(t *testing.T) {
	cfg := cfgWith(map[string]*profile.Profile{"dev": {Name: "dev"}}, "dev")
	_, _, err := profile.Resolve("ghost", "", cfg)
	if !errors.Is(err, profile.ErrProfileNotFound) {
		t.Fatalf("want ErrProfileNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "--profile") {
		t.Errorf("message should name the bad value and the flag level: %v", err)
	}
}

func TestResolveChain_ProjectHintNotFound(t *testing.T) {
	cfg := cfgWith(map[string]*profile.Profile{"dev": {Name: "dev"}}, "dev")
	_, _, err := profile.Resolve("", "absent", cfg)
	if !errors.Is(err, profile.ErrProfileNotFound) {
		t.Fatalf("want ErrProfileNotFound, got %v", err)
	}
	// The shared not-found error type, but the message points at the
	// project file so the user can find the bad sparkwing.yaml.
	if !strings.Contains(err.Error(), "absent") || !strings.Contains(err.Error(), "sparkwing.yaml") {
		t.Errorf("message should name the bad value and the project level: %v", err)
	}
}

func TestBuiltinLaptopProfile_Surfaces(t *testing.T) {
	p := profile.BuiltinLaptopProfile()
	s := p.Surfaces()
	if s.State == nil || s.State.Type != backends.TypeSQLite {
		t.Errorf("state = %#v, want sqlite", s.State)
	}
	if s.State.Path != "" {
		t.Errorf("state path = %q, want empty (caller fills via Paths)", s.State.Path)
	}
	if s.Cache == nil || s.Cache.Type != backends.TypeFilesystem || s.Cache.Path != "~/.cache/sparkwing" {
		t.Errorf("cache = %#v, want filesystem ~/.cache/sparkwing", s.Cache)
	}
	if s.Logs == nil || s.Logs.Type != backends.TypeFilesystem || s.Logs.Path != "~/.cache/sparkwing/logs" {
		t.Errorf("logs = %#v, want filesystem ~/.cache/sparkwing/logs", s.Logs)
	}
	if p.Controller != "" || p.Token != "" {
		t.Errorf("laptop should carry no controller/token: %#v", p)
	}
}

func consideredHas(c profile.Chain, src profile.ChainSource, name string) bool {
	for _, e := range c.Considered {
		if e.Source == src && e.Name == name {
			return true
		}
	}
	return false
}
