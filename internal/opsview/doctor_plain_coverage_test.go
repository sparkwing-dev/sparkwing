package opsview

import (
	"bytes"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/githooks"
)

func TestDoctorPlainReportsEveryConditionThatMakesAHomeUnclean(t *testing.T) {
	marks := map[string]func(*DoctorReport){
		"PermissionRepairs":         func(r *DoctorReport) { r.PermissionRepairs = []fssecure.Change{{Path: "p"}} },
		"PermissionAuditUnverified": func(r *DoctorReport) { r.PermissionAuditUnverified = true },
		"OrphanedRuns":              func(r *DoctorReport) { r.OrphanedRuns = []string{"run-1"} },
		"LegacyBoxSlotFilesRemoved": func(r *DoctorReport) { r.LegacyBoxSlotFilesRemoved = 1 },
		"LiveLegacyHolders":         func(r *DoctorReport) { r.LiveLegacyHolders = []DoctorLegacyHolder{{}} },
		"DeadConcurrencyHolders":    func(r *DoctorReport) { r.DeadConcurrencyHolders = 1 },
		"DeadConcurrencyWaiters":    func(r *DoctorReport) { r.DeadConcurrencyWaiters = 1 },
		"DanglingRunDirs":           func(r *DoctorReport) { r.DanglingRunDirs = []string{"run-2"} },
		"StaleRefWorktrees":         func(r *DoctorReport) { r.StaleRefWorktrees = []string{"run-3"} },
		"AdmissionRejections":       func(r *DoctorReport) { r.AdmissionRejections = []DoctorRejection{{Count: 1}} },
		"DaemonVersionSkew":         func(r *DoctorReport) { r.DaemonVersionSkew = &DoctorVersionSkew{} },
		"LockedOutRepos":            func(r *DoctorReport) { r.LockedOutRepos = []DoctorLockedOutRepo{{}} },
		"DaemonProtocolGap":         func(r *DoctorReport) { r.DaemonProtocolGap = &DoctorProtocolGap{} },
		"QuarantinedLedgers":        func(r *DoctorReport) { r.QuarantinedLedgers = []string{"l"} },
		"PoisonedProfiles":          func(r *DoctorReport) { r.PoisonedProfiles = []DoctorPoisonedProfile{{}} },
		"InstallConflict": func(r *DoctorReport) {
			r.InstallConflict = &DoctorInstallConflict{Competing: []DoctorInstallCopy{{Path: "p"}}}
		},
		"ShadowedHooks": func(r *DoctorReport) {
			r.ShadowedHooks = &githooks.Shadow{Gates: []string{"pre-push"}}
		},
	}

	var baseline bytes.Buffer
	if err := renderDoctorPlain(&baseline, DoctorReport{}); err != nil {
		t.Fatalf("renderDoctorPlain: %v", err)
	}

	for name, mark := range marks {
		t.Run(name, func(t *testing.T) {
			report := DoctorReport{}
			mark(&report)
			if report.Clean() {
				t.Fatalf("%s does not make a report unclean; this test's fixture no longer matches Clean", name)
			}
			var got bytes.Buffer
			if err := renderDoctorPlain(&got, report); err != nil {
				t.Fatalf("renderDoctorPlain: %v", err)
			}
			if got.String() == baseline.String() {
				t.Errorf("plain output for an unclean %s is identical to a clean home's, so an "+
					"operator reading -o plain sees nothing to act on while the verdict says unclean", name)
			}
		})
	}
}
