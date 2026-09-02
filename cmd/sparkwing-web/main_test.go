package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	swpaths "github.com/sparkwing-dev/sparkwing/internal/paths"
)

func TestValidateLoginBackend(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		require    bool
		controller string
		wantErr    bool
	}{
		{name: "login free local"},
		{name: "login free malformed controller", controller: "not-a-url"},
		{name: "required controller", require: true, controller: "https://controller.example"},
		{name: "required missing controller", require: true, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateLoginBackend(test.require, test.controller)
			if test.wantErr && (err == nil || !strings.Contains(err.Error(), "--require-login requires")) {
				t.Fatalf("error = %v, want actionable require-login error", err)
			}
			if !test.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResolveAuthControllerURL(t *testing.T) {
	t.Parallel()
	if got := resolveAuthControllerURL("https://explicit.example", "https://profile.example"); got != "https://explicit.example" {
		t.Fatalf("explicit controller = %q", got)
	}
	if got := resolveAuthControllerURL("", "https://profile.example"); got != "https://profile.example" {
		t.Fatalf("profile controller = %q", got)
	}
}

func TestRunRejectsMalformedTrustedProxyCIDRs(t *testing.T) {
	err := run([]string{"--trusted-proxy-cidrs", "10.0.0.1"})
	if err == nil || !strings.Contains(err.Error(), "--trusted-proxy-cidrs") {
		t.Fatalf("error = %v, want trusted proxy CIDR error", err)
	}
}

func TestOpenFromConfigReturnsProfileSessionController(t *testing.T) {
	root := t.TempDir()
	profilesPath := filepath.Join(root, "profiles.yaml")
	statePath := filepath.Join(root, "state.db")
	contents := "profiles:\n" +
		"  prod:\n" +
		"    controller:\n" +
		"      url: https://controller.example\n" +
		"      token: service-token\n" +
		"    state:\n" +
		"      type: sqlite\n" +
		"      path: " + statePath + "\n"
	if err := os.WriteFile(profilesPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_PROFILES", profilesPath)

	b, closer, controllerURL, err := openFromConfig(
		context.Background(),
		swpaths.PathsAt(filepath.Join(root, "home")),
		"prod", "", "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	if b == nil {
		t.Fatal("profile backend is nil")
	}
	if controllerURL != "https://controller.example" {
		t.Fatalf("session controller = %q", controllerURL)
	}
}

func TestRunFailsClosedWithoutUsableSessionController(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SPARKWING_HOME", filepath.Join(root, "home"))
	stateSpec := "sqlite://" + filepath.Join(root, "state.db")
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing controller",
			args:    []string{"--require-login", "--state-spec", stateSpec},
			wantErr: "--require-login requires a controller session backend",
		},
		{
			name: "malformed controller",
			args: []string{
				"--require-login",
				"--controller", "ftp://controller.example",
				"--state-spec", stateSpec,
			},
			wantErr: "controller session backend must be an absolute http(s) URL",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := run(test.args)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("run(%q) error = %v, want %q", test.args, err, test.wantErr)
			}
		})
	}
}
