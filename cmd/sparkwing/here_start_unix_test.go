//go:build !windows

package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/sparkwing/testsupport/testhome"
)

func TestHereTriggerStartsAdmissionBeforeRemoteIntake(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a detached local controller")
	}
	testhome.Set(t, testhome.TempDir(t))
	home := testhome.TempDir(t)
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", testhome.TempDir(t))
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "missing.yaml"))
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(home, "secrets.key"))
	offlineStopDaemon(t, home)
	if _, err := wingdclient.QueryLocalController(t.Context(), home); !errors.Is(err, wingdclient.ErrNoDaemon) {
		t.Fatalf("expected a fresh home without a daemon: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	intake := errors.New("remote intake reached")
	called := false
	err := runTriggerCommandArgs(ctx, []string{"deploy-prod", "--profile=remote", "--runner=here", "--reason=inspect"}, io.Discard,
		func(context.Context, triggerFlags) (controllerwire.CreateRunRequest, error) {
			return initialTriggerRequest(true), nil
		},
		func(context.Context, string) (client.EngineOptions, func(), error) {
			return client.EngineOptions{URL: "https://controller.example", HTTP: &http.Client{Transport: profileTransport(func(*http.Request) (*http.Response, error) {
				called = true
				if status, err := wingdclient.QueryLocalController(ctx, home); err != nil || !status.Running {
					t.Errorf("remote intake preceded local admission readiness: %+v: %v", status, err)
				}
				return nil, intake
			})}}, func() {}, nil
		})
	if !called || !errors.Is(err, intake) {
		t.Fatalf("remote intake: called=%v err=%v", called, err)
	}
}

func TestHereTriggerStartupFailurePreventsRemoteIntake(t *testing.T) {
	testhome.Set(t, testhome.TempDir(t))
	home := filepath.Join(testhome.TempDir(t), "not-a-directory")
	if err := os.WriteFile(home, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(testhome.TempDir(t), "missing.yaml"))
	err := runTriggerCommandArgs(t.Context(), []string{"deploy-prod", "--profile=remote", "--runner=here", "--reason=inspect"}, io.Discard,
		func(context.Context, triggerFlags) (controllerwire.CreateRunRequest, error) {
			return initialTriggerRequest(true), nil
		},
		func(context.Context, string) (client.EngineOptions, func(), error) {
			t.Fatal("failed local admission startup reached remote intake")
			return client.EngineOptions{}, nil, nil
		})
	if err == nil || !strings.Contains(err.Error(), "sparkwing dashboard open") {
		t.Fatalf("startup failure has no repair: %v", err)
	}
}
