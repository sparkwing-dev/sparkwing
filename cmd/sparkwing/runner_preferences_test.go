package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestRunnerEnrollmentPreservesUnsetPreferences(t *testing.T) {
	f := newRunnersFixture(t)
	enrollment, _, err := prepareRunnerEnrollmentArgs([]string{"--profile", "prod", "--runner", "desktop", "--config", f.config, "--no-service"})
	if err != nil {
		t.Fatal(err)
	}
	if err := enrollRunner(t.Context(), f, enrollment); err != nil {
		t.Fatal(err)
	}
	cfg, err := userconfig.ReadConfig(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runner == nil || cfg.Runner.MaxConcurrent != nil {
		t.Fatalf("enrollment filled an absent concurrency preference: %+v", cfg.Runner)
	}
	resolved, _, err := settings.RunnerPreferences(settings.Inputs{Machine: settings.Values{RunnerPreferences: cfg.Runner}})
	if err != nil || resolved.MaxConcurrent == nil || *resolved.MaxConcurrent != 1 {
		t.Fatalf("shared runner defaults: %+v %v", resolved, err)
	}
}

func TestRunnerEnrollmentUsesCompleteMachineSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SPARKWING_CONFIG", path)
	cfg := userconfig.RunnerConfig{
		Controller: preferencePointer("https://controller.example"), HolderPrefix: preferencePointer("desktop"),
		MaxConcurrent: preferencePointer(3), Repos: []string{"github.com/acme/*"}, Labels: []string{},
		AcceptWorkingTree: preferencePointer(false), LocalAdmission: preferencePointer(false),
		Poll: preferencePointer("1s"), Lease: preferencePointer("30s"),
		Heartbeat: preferencePointer("10s"), SpawnPolicy: preferencePointer("return-to-queue"),
	}
	written, err := writeRunnerConfig(path, cfg, "private-runner-token", false)
	if err != nil {
		t.Fatal(err)
	}
	final, err := userconfig.ReadConfig(path)
	if err != nil || final.Runner == nil {
		t.Fatalf("read config: %v", err)
	}
	cfg.CredentialsDir = &written.credentialsDir
	if !reflect.DeepEqual(*final.Runner, cfg) {
		t.Fatalf("runner preferences changed: got %+v want %+v", *final.Runner, cfg)
	}
}

func preferencePointer[T any](value T) *T { return &value }

func TestRunnerEnrollmentRollbackPreservesExplicitEmptyPreferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SPARKWING_CONFIG", path)
	cfg := userconfig.RunnerConfig{Controller: preferencePointer("https://controller.example"), Labels: []string{}, Repos: []string{}}
	if _, err := writeRunnerConfig(path, cfg, "first-private-credential", false); err != nil {
		t.Fatal(err)
	}
	previous, err := userconfig.ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	written, err := writeRunnerConfig(path, userconfig.RunnerConfig{Controller: cfg.Controller}, "second-private-credential", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := written.rollback(); err != nil {
		t.Fatal(err)
	}
	current, err := userconfig.ReadConfig(path)
	if err != nil || !reflect.DeepEqual(current.Runner, previous.Runner) {
		t.Fatalf("rollback changed explicit preferences: %+v %v", current.Runner, err)
	}
}

func TestRunnerRemovalRefusesPublicCredentialBeforeStoppingOrRevoking(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file modes")
	}
	f := newRunnersFixture(t)
	if err := f.run([]string{"add", "--profile", "prod", "--runner", "desktop", "--config", f.config, "--no-service"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := userconfig.ReadConfig(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(*cfg.Runner.CredentialsDir, "agent-token"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.run([]string{"remove", "--profile", "prod", "--config", f.config}); err == nil {
		t.Fatal("public credential file was accepted")
	}
	if len(*f.calls) != 0 || f.tokens[0].RevokedAt != nil {
		t.Fatal("credential refusal stopped the service or revoked authority")
	}
}

func TestRunnerRemovalUnsafeModesCannotStopOrRevokeProperty(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file modes")
	}
	f := newRunnersFixture(t)
	if err := f.run([]string{"add", "--profile", "prod", "--runner", "desktop", "--config", f.config, "--no-service"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := userconfig.ReadConfig(f.config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(*cfg.Runner.CredentialsDir, "agent-token")
	property := func(bits uint8) bool {
		mode := os.FileMode(0o600 | 1<<uint(bits%6))
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		err := f.run([]string{"remove", "--profile", "prod", "--config", f.config})
		return err != nil && len(*f.calls) == 0 && f.tokens[0].RevokedAt == nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
}

func FuzzRunnerEnrollmentKeepsBearerPrivate(f *testing.F) {
	f.Add("private-credential")
	f.Add("token: with\nYAML [fields] and \x00 bytes")
	f.Fuzz(func(t *testing.T, value string) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		t.Setenv("SPARKWING_CONFIG", path)
		bearer := "swk_private_" + base64.RawURLEncoding.EncodeToString([]byte(value))
		cfg := userconfig.RunnerConfig{Controller: preferencePointer("https://controller.example"), HolderPrefix: preferencePointer("desktop")}
		written, err := writeRunnerConfig(path, cfg, bearer, false)
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(path)
		if err != nil || strings.Contains(string(body), bearer) {
			t.Fatal("runner YAML exposed the bearer or was unreadable")
		}
		file, err := fssecure.OpenPrivateConfig(filepath.Join(written.credentialsDir, "agent-token"))
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		credential, err := os.ReadFile(filepath.Join(written.credentialsDir, "agent-token"))
		if err != nil || string(credential) != bearer || cfg.CredentialsDir != nil {
			t.Fatal("credential persistence changed the bearer or input preferences")
		}
	})
}
