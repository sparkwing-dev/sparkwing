package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
)

func TestPipelineModuleCommandsHonorToolchainFloor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Go uses a POSIX shell")
	}
	calls := []struct {
		name string
		run  func(context.Context, string) error
	}{
		{"init tidy", func(ctx context.Context, dir string) error { _, err := tidySkeleton(ctx, dir); return err }},
		{"SDK get", func(ctx context.Context, dir string) error {
			_, err := runGoModCmd(ctx, dir, "get", sdkModulePath+"@v0.49.0")
			return err
		}},
	}
	for _, call := range calls {
		for _, setting := range []string{"go1.26.6", "local"} {
			t.Run(call.name+"/"+setting, func(t *testing.T) {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/pipeline\ngo 1.26.8\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				log := filepath.Join(dir, "commands")
				script := "#!/bin/sh\nif [ \"$1\" = env ]; then printf '%s\\ngo1.26.6\\noff\\n' \"$GOTOOLCHAIN\"; exit 0; fi\nprintf '%s\\n' \"$GOTOOLCHAIN\" >> \"$COMMAND_LOG\"\n"
				if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", dir)
				t.Setenv("GOTOOLCHAIN", setting)
				t.Setenv("COMMAND_LOG", log)
				ctx := gotoolchain.WithSession(context.Background(), nil, func(string) {})
				err := call.run(ctx, dir)
				if setting == "local" {
					var blocked *gotoolchain.Error
					if !errors.As(err, &blocked) {
						t.Fatalf("error = %v, want typed floor error", err)
					}
					if _, err := os.Stat(log); !os.IsNotExist(err) {
						t.Fatalf("blocked command ran: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(log)
				if err != nil {
					t.Fatal(err)
				}
				if strings.TrimSpace(string(data)) != "go1.26.8" {
					t.Fatalf("build pin = %q", data)
				}
				if os.Getenv("GOTOOLCHAIN") != setting {
					t.Fatal("changed original pin")
				}
			})
		}
	}
}
