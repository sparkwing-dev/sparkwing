package sources_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/sources"
)

func TestValidate_AcceptsEverySourceType(t *testing.T) {
	f := sources.File{
		Default: "team",
		Sources: map[string]sources.Source{
			"team":  {Type: sources.TypeProfile, Profile: "shared"},
			"dot":   {Type: sources.TypeFile, Path: ".env"},
			"shell": {Type: sources.TypeEnv, Prefix: "SW_"},
		},
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidate_ProfileTypeRequiresProfileField(t *testing.T) {
	f := sources.File{Sources: map[string]sources.Source{
		"team": {Type: sources.TypeProfile},
	}}
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "profile") {
		t.Fatalf("Validate: want profile-required error, got %v", err)
	}
}

func TestValidate_RejectsUnknownType(t *testing.T) {
	f := sources.File{Sources: map[string]sources.Source{
		"x": {Type: "vault-pro"},
	}}
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("Validate: want unknown-type error, got %v", err)
	}
}

func TestValidate_FileTypeRequiresPath(t *testing.T) {
	f := sources.File{Sources: map[string]sources.Source{
		"dot": {Type: sources.TypeFile},
	}}
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("Validate: want path-required error, got %v", err)
	}
}

func TestValidate_DefaultMustBeDeclared(t *testing.T) {
	f := sources.File{
		Default: "ghost",
		Sources: map[string]sources.Source{"dot": {Type: sources.TypeFile, Path: ".env"}},
	}
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "default") {
		t.Fatalf("Validate: want default-not-declared error, got %v", err)
	}
}
