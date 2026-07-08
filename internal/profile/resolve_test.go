package profile_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

func cfgWith(profiles map[string]*profile.Profile) *profile.Config {
	return &profile.Config{Profiles: profiles}
}

func TestResolve_FlagSelects(t *testing.T) {
	cfg := cfgWith(map[string]*profile.Profile{
		"prod": {Name: "prod"},
		"dev":  {Name: "dev"},
	})
	p, chain, err := profile.Resolve("prod", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.Name != "prod" {
		t.Fatalf("profile = %#v, want prod", p)
	}
	if chain.Selected != "prod" || chain.Source != profile.ChainSourceFlag {
		t.Fatalf("chain = %+v, want prod via flag", chain)
	}
}

func TestResolve_EmptyFlagYieldsNilProfile(t *testing.T) {
	cfg := cfgWith(map[string]*profile.Profile{"dev": {Name: "dev"}})
	p, chain, err := profile.Resolve("", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatalf("expected nil profile (project defaults apply), got %#v", p)
	}
	if chain.Source != profile.ChainSourceNone {
		t.Errorf("chain.Source = %q, want %q", chain.Source, profile.ChainSourceNone)
	}
}

func TestResolve_FlagNotFound(t *testing.T) {
	cfg := cfgWith(map[string]*profile.Profile{"dev": {Name: "dev"}})
	_, _, err := profile.Resolve("ghost", cfg)
	if !errors.Is(err, profile.ErrProfileNotFound) {
		t.Fatalf("want ErrProfileNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "--profile") {
		t.Errorf("message should name the bad value and the flag: %v", err)
	}
}

func TestResolve_NilConfigWithFlag(t *testing.T) {
	_, _, err := profile.Resolve("anything", nil)
	if !errors.Is(err, profile.ErrProfileNotFound) {
		t.Fatalf("want ErrProfileNotFound, got %v", err)
	}
}
