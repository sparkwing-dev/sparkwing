package sources_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/sources"
)

func TestValidate_AcceptsEverySourceType(t *testing.T) {
	cases := []sources.Source{
		{Type: sources.TypeController, URL: "https://controller.shared.example.com"},
		{Type: sources.TypeFile, Path: ".env"},
		{Type: sources.TypeEnv, Prefix: "SW_"},
		{Type: sources.TypeEnv},
	}
	for _, s := range cases {
		if err := s.Validate(); err != nil {
			t.Errorf("Validate(%+v): %v", s, err)
		}
	}
}

func TestValidate_ControllerTypeRequiresURL(t *testing.T) {
	s := sources.Source{Type: sources.TypeController}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "url") {
		t.Fatalf("Validate: want url-required error, got %v", err)
	}
}

func TestValidate_FileTypeRequiresPath(t *testing.T) {
	s := sources.Source{Type: sources.TypeFile}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("Validate: want path-required error, got %v", err)
	}
}

func TestValidate_RejectsUnknownType(t *testing.T) {
	s := sources.Source{Type: "vault-pro"}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("Validate: want unknown-type error, got %v", err)
	}
}

func TestValidate_RejectsEmptyType(t *testing.T) {
	if err := (sources.Source{}).Validate(); err == nil || !strings.Contains(err.Error(), "type is required") {
		t.Fatalf("Validate: want type-required error, got %v", err)
	}
}

func TestDescribe(t *testing.T) {
	cases := []struct {
		s    sources.Source
		want string
	}{
		{sources.Source{Type: sources.TypeController, URL: "https://controller.prod.example.com"}, "controller:https://controller.prod.example.com"},
		{sources.Source{Type: sources.TypeFile, Path: ".env"}, "file:.env"},
		{sources.Source{Type: sources.TypeEnv, Prefix: "SW_"}, "env:SW_"},
		{sources.Source{Type: sources.TypeEnv}, "env"},
		{sources.Source{}, ""},
	}
	for _, tc := range cases {
		if got := tc.s.Describe(); got != tc.want {
			t.Errorf("Describe(%+v) = %q, want %q", tc.s, got, tc.want)
		}
	}
}
