//go:build windows

package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestLocalDispatchAdmissionUsesChildHome(t *testing.T) {
	home := wingdTestHome(t)
	startWingd(t, home, 2)
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv(wingdclient.HostBinEnv, filepath.Join(t.TempDir(), "missing.exe"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	closeAdmission, err := prepareLocalDispatchAdmission(ctx, []string{"sparkwing_home=" + home})
	if err != nil {
		t.Fatal(err)
	}
	closeAdmission()
	if _, err := wingdclient.Query(ctx, wingdclient.Options{Home: home, Version: "test"}); err != nil {
		t.Fatalf("closing dispatch admission stopped shared daemon: %v", err)
	}
}

func TestLocalDispatchAdmissionSurfacesUnusableHost(t *testing.T) {
	t.Setenv(wingdclient.HostBinEnv, filepath.Join(t.TempDir(), "missing.exe"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	closeAdmission, err := prepareLocalDispatchAdmission(ctx, []string{"SPARKWING_HOME=" + wingdTestHome(t)})
	if !errors.Is(err, wingdclient.ErrDaemonHostUnusable) || closeAdmission != nil {
		t.Fatalf("prepare admission = %v, %v; want unusable host", closeAdmission != nil, err)
	}
}

func TestLocalDispatchAdmissionPreservesNoHostFallback(t *testing.T) {
	t.Setenv(wingdclient.HostBinEnv, "")
	closeAdmission, err := prepareLocalDispatchAdmission(context.Background(), nil)
	if err != nil || closeAdmission == nil {
		t.Fatalf("no-host fallback = %v, %v", closeAdmission != nil, err)
	}
	closeAdmission()
}
