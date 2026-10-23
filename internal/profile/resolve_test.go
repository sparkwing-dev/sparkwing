package profile

import (
	"errors"
	"strings"
	"testing"
)

func cfgWith(profiles map[string]*profile) *config {
	return &config{Profiles: profiles}
}

func TestResolve_FlagSelects(t *testing.T) {
	cfg := cfgWith(map[string]*profile{
		"prod": {Name: "prod"},
		"dev":  {Name: "dev"},
	})
	p, chain, err := resolve("prod", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.Name != "prod" {
		t.Fatalf("profile = %#v, want prod", p)
	}
	if chain.Selected != "prod" || chain.Source != chainSourceFlag {
		t.Fatalf("chain = %+v, want prod via flag", chain)
	}
}

func TestResolve_EmptyFlagYieldsNilProfile(t *testing.T) {
	cfg := cfgWith(map[string]*profile{"dev": {Name: "dev"}})
	p, chain, err := resolve("", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatalf("expected nil profile (project defaults apply), got %#v", p)
	}
	if chain.Source != chainSourceNone {
		t.Errorf("chain.Source = %q, want %q", chain.Source, chainSourceNone)
	}
}

func TestResolve_FlagNotFound(t *testing.T) {
	cfg := cfgWith(map[string]*profile{"dev": {Name: "dev"}})
	_, _, err := resolve("ghost", cfg)
	if !errors.Is(err, errProfileNotFound) {
		t.Fatalf("want ErrProfileNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "--profile") {
		t.Errorf("message should name the bad value and the flag: %v", err)
	}
}

func TestResolve_NilConfigWithFlag(t *testing.T) {
	_, _, err := resolve("anything", nil)
	if !errors.Is(err, errProfileNotFound) {
		t.Fatalf("want ErrProfileNotFound, got %v", err)
	}
}
