package main

import (
	"context"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"

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

func TestRunRejectsTokenWithoutABackend(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SPARKWING_HOME", filepath.Join(root, "home"))
	t.Setenv("SPARKWING_AGENT_TOKEN", "")

	err := run([]string{"--token", "service-token", "--addr", "127.0.0.1:0"})
	if err == nil || !strings.Contains(err.Error(), "--token") ||
		!strings.Contains(err.Error(), "--controller") {
		t.Fatalf("error = %v, want the missing-backend refusal naming --controller", err)
	}
}

func TestOpenFromConfigReturnsProfileSessionController(t *testing.T) {
	root := t.TempDir()
	profilesPath := filepath.Join(root, "config.yaml")
	statePath := filepath.Join(root, "state.db")
	contents := "profiles:\n" +
		"  prod:\n" +
		"    controller:\n" +
		"      url: https://controller.example\n" +
		"      token: service-token\n" +
		"    state:\n" +
		"      type: sqlite\n" +
		"      path: " + statePath + "\n"
	if err := fssecure.SecurePrivateDir(root); err != nil {
		t.Fatal(err)
	}
	if err := fssecure.WriteFile(profilesPath, []byte(contents)); err != nil {
		t.Fatal(err)
	}
	if err := fssecure.SecurePrivateConfig(profilesPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", profilesPath)

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
	stateSpec := (&url.URL{Scheme: "sqlite", Path: "/" + strings.TrimPrefix(filepath.ToSlash(filepath.Join(root, "state.db")), "/")}).String()
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
