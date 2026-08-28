//go:build windows

package opsview_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

func TestDiagnoseReportsWindowsPermissionAuditUnverified(t *testing.T) {
	root := t.TempDir()
	report, err := opsview.Diagnose(context.Background(), paths.PathsAt(root), root, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !report.PermissionAuditUnverified {
		t.Fatal("Windows permission audit did not report unverified")
	}
	if report.Clean() {
		t.Fatal("unverified Windows permission audit reported clean")
	}
}
