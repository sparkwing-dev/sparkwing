package opsview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/boxslot"
	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/githooks"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// doctorRunOrphanGrace is how long a running run row must have gone without a
// heartbeat before doctor treats it as orphaned. A live run heartbeats well
// inside this window, so the grace keeps a briefly-busy run from being
// finalized out from under itself.
const doctorRunOrphanGrace = 2 * time.Minute

// doctorRejectionPatternThreshold is how many malformed-request rejections
// of one cause the daemon must have tallied in its outcome window before
// doctor calls it a pattern worth surfacing. A lone rejection is noise; a
// repeat is a standing misconfiguration or version skew.
const doctorRejectionPatternThreshold = 3

// DoctorReport is what a doctor sweep found and repaired, and the wire shape
// of its -o json output.
type DoctorReport struct {
	// DryRun reports that nothing was changed; the counts are what would have
	// been repaired.
	DryRun bool `json:"dry_run"`
	// OrphanedRuns are run ids that were marked running with no live process
	// and no daemon lease, finalized as interrupted.
	OrphanedRuns []string `json:"orphaned_runs,omitempty"`
	// LegacyBoxSlotFilesRemoved counts lock files cleared from an idle legacy
	// box-slot directory.
	LegacyBoxSlotFilesRemoved int `json:"legacy_box_slot_files_removed"`
	// LiveLegacyHolders are box-slot locks still held by an older-pinned
	// pipeline binary admitting outside the daemon -- reported, never removed.
	LiveLegacyHolders []DoctorLegacyHolder `json:"live_legacy_holders,omitempty"`
	// DeadConcurrencyHolders and DeadConcurrencyWaiters count local-scope
	// concurrency rows removed because their run has ended.
	DeadConcurrencyHolders int `json:"dead_concurrency_holders"`
	DeadConcurrencyWaiters int `json:"dead_concurrency_waiters"`
	// DanglingRunDirs are run artifact directories removed because their run
	// row no longer exists.
	DanglingRunDirs []string `json:"dangling_run_dirs,omitempty"`
	// AdmissionRejections are repeated malformed-request rejections the local
	// admission daemon tallied in its outcome window -- a standing pattern,
	// not a one-off. Reported with an explanation, never repaired.
	AdmissionRejections []DoctorRejection `json:"admission_rejections,omitempty"`
	// DaemonVersionSkew is set when the running binary and the live admission
	// daemon are different builds -- a skew that does not always resolve by
	// takeover and can leave the daemon rejecting a newer client's requests.
	// Reported with an explanation, never repaired.
	DaemonVersionSkew *DoctorVersionSkew `json:"daemon_version_skew,omitempty"`
	// LockedOutRepos are registered repos whose SDK pin sits below the release
	// that speaks to the resident daemon, so every gate they run is refused
	// until the pin moves. This is the skew that blocks work: DaemonVersionSkew
	// compares the CLI, which is not a party to a pipeline's handshake.
	// Reported with the pin to raise, never repaired -- editing another repo's
	// go.mod is not doctor's call.
	LockedOutRepos []DoctorLockedOutRepo `json:"locked_out_repos,omitempty"`
	// DaemonProtocolGap is set when the resident daemon speaks a wire protocol
	// major newer than any this build's release table covers. This binary then
	// cannot query that daemon at all, and cannot name the release that first
	// spoke its major, so LockedOutRepos is measured against the daemon's own
	// release rather than the lowest one that would do. Reported with the
	// remedy -- update this CLI -- never repaired.
	DaemonProtocolGap *DoctorProtocolGap `json:"daemon_protocol_gap,omitempty"`
	// QuarantinedLedgers are admission state files the daemon moved aside
	// because it could not restore them, serving with a fresh ledger instead.
	// They are forensic copies: reported with an explanation, never removed.
	QuarantinedLedgers []string `json:"quarantined_ledgers,omitempty"`
	// PoisonedProfiles are capacity profiles whose contended-run demand floor
	// prices every run at or above the machine's grantable ceiling, so the
	// pipeline serializes the whole box until the floor decays or an operator
	// resets the profile. Reported with the reset command, never repaired --
	// discarding learned measurements is the operator's call.
	PoisonedProfiles []DoctorPoisonedProfile `json:"poisoned_profiles,omitempty"`
	// ShadowedHooks is set when the checkout doctor runs in has sparkwing
	// hooks installed that git will not run, because core.hooksPath points at
	// another directory -- a commit or push gate that is silently gone.
	// Reported with the fix, never repaired: rewriting an operator's git
	// config is not doctor's call.
	ShadowedHooks *githooks.Shadow `json:"shadowed_hooks,omitempty"`
	// UngatedRepos are the registered checkouts whose pipelines declare a
	// commit or push hook that git runs nothing for -- shadowed by a
	// core.hooksPath, or never installed. It answers for the whole registry
	// rather than the checkout doctor was run from, so a repo registered
	// after the last sweep reports itself instead of waiting to be noticed.
	// Reported with the fix, never repaired: arming a repo runs its gate,
	// and a gate that cannot run turns every commit there into a failure.
	UngatedRepos []githooks.RepoGates `json:"ungated_repos,omitempty"`
	// StrayDaemons are admission daemons serving other sparkwing homes that
	// report a version no released build carries -- scratch binaries left
	// running, which look like the machine's resident daemon in a process
	// list. Reported with an explanation, never stopped: doctor does not end
	// processes it does not own.
	StrayDaemons []DoctorStrayDaemon `json:"stray_daemons,omitempty"`
}

// DoctorStrayDaemon is one live admission daemon serving another sparkwing
// home whose reported version marks it as a scratch build.
type DoctorStrayDaemon struct {
	// Socket is the address the daemon answered on.
	Socket string `json:"socket"`
	// Version is the version it reported for its own build.
	Version string `json:"version"`
}

// DoctorPoisonedProfile is one contention-poisoned capacity profile in the
// report: the stored profile key, the floor and the charge it prices, and
// the grantable ceiling that charge meets or exceeds.
type DoctorPoisonedProfile struct {
	Pipeline       string  `json:"pipeline"`
	FloorCores     float64 `json:"floor_cores"`
	ChargeCores    float64 `json:"charge_cores"`
	GrantableCores float64 `json:"grantable_cores"`
}

// DoctorRejection is one repeated malformed-request rejection cause in the
// report: the stable cause label the daemon tallied and how many times it
// fired in the window.
type DoctorRejection struct {
	Cause string `json:"cause"`
	Count int    `json:"count"`
}

// DoctorVersionSkew names a mismatch between the running binary and the live
// admission daemon it talks to.
type DoctorVersionSkew struct {
	// Self is the running binary's version.
	Self string `json:"self"`
	// Daemon is the live daemon's reported version.
	Daemon string `json:"daemon"`
}

// DoctorLockedOutRepo is one registered repo whose pinned SDK sits below the
// release that speaks to the resident daemon, so the daemon refuses it and no
// takeover can resolve that, because takeover runs from the client side.
type DoctorLockedOutRepo struct {
	// Name is the checkout directory's base name.
	Name string `json:"name"`
	// Path is the registered checkout root whose .sparkwing/go.mod carries
	// Pin. A linked worktree gets its own row rather than folding into its
	// primary: the pipeline binary is built from the .sparkwing of whichever
	// checkout runs the gate, and a worktree's pin can differ from the
	// primary's, so canonicalizing here would report a pin nothing runs and
	// hide the one that is refused.
	Path string `json:"path"`
	// Worktree marks a row whose Path is a linked worktree, so a Name that
	// reads as a branch rather than a repo is not a surprise.
	Worktree bool `json:"worktree,omitempty"`
	// Pin is the SDK version in the checkout's .sparkwing/go.mod.
	Pin string `json:"pin"`
	// RaiseTo is the SDK release this pin has to reach to speak to the
	// resident daemon: the lowest release at the daemon's protocol major,
	// or -- when this build's table ends below that major, which
	// [DoctorReport.DaemonProtocolGap] then reports -- the daemon's own
	// release, which speaks it but need not be the lowest that does.
	RaiseTo string `json:"raise_to"`
}

// DoctorProtocolGap names a resident daemon speaking a wire protocol major
// this build does not know, which is both why this CLI cannot query it and
// why a lockout diagnosis against it can only give a floor.
type DoctorProtocolGap struct {
	// Self is the newest protocol major this build's release table covers,
	// which is the major this build speaks.
	Self int `json:"self"`
	// Daemon is the protocol major the daemon reported in its handshake ack.
	Daemon int `json:"daemon"`
	// DaemonVersion is the release the daemon reports for its own build.
	DaemonVersion string `json:"daemon_version"`
}

// DoctorLegacyHolder is one live legacy box-slot holder in the report.
type DoctorLegacyHolder struct {
	PID   int    `json:"pid"`
	RunID string `json:"run_id,omitempty"`
	Lock  string `json:"lock"`
}

// Clean reports whether the sweep found nothing to repair and no legacy binary
// admitting outside the daemon. It answers for the home that was swept, so
// [DoctorReport.StrayDaemons] -- other homes' processes, which this home's
// state says nothing about -- deliberately does not count: a clean home
// stays clean whatever else is running on the machine, and the stray is
// reported alongside rather than folded in.
//
// [DoctorReport.LockedOutRepos] counts because the refusing daemon belongs to
// this home: a pin that sits below what this home's daemon speaks is a defect
// in this home's configuration, not a property of some other home.
//
// [DoctorReport.UngatedRepos] is excluded because the question of which
// repositories git-gate their pipelines is answered by the machine's git
// configuration, not by this home's daemon. It is rendered on the healthy path
// too, so an ungated repo is still reported by a run that finds nothing to
// repair.
func (r DoctorReport) Clean() bool {
	return len(r.OrphanedRuns) == 0 &&
		r.LegacyBoxSlotFilesRemoved == 0 &&
		len(r.LiveLegacyHolders) == 0 &&
		r.DeadConcurrencyHolders == 0 &&
		r.DeadConcurrencyWaiters == 0 &&
		len(r.DanglingRunDirs) == 0 &&
		len(r.AdmissionRejections) == 0 &&
		r.DaemonVersionSkew == nil &&
		len(r.LockedOutRepos) == 0 &&
		r.DaemonProtocolGap == nil &&
		len(r.QuarantinedLedgers) == 0 &&
		len(r.PoisonedProfiles) == 0 &&
		r.ShadowedHooks == nil
}

// Diagnose runs every doctor check against the sparkwing home and repairs what
// it safely can (unless dryRun). It never returns early on a single check's
// failure so a healthy check still reports even if another errors. selfVersion
// is the running binary's own version, compared against the live daemon's to
// flag a version skew; pass "" to skip that check.
func Diagnose(ctx context.Context, p paths.Paths, home, selfVersion string, dryRun bool) (DoctorReport, error) {
	report := DoctorReport{DryRun: dryRun}

	daemonLive := liveDaemonRuns(ctx, home)

	boxHolders, err := boxslot.Holders(p.BoxSlotDir())
	if err != nil {
		return report, err
	}
	legacyRuns := map[string]struct{}{}
	for _, h := range boxHolders {
		if h.Live && h.RunID != "" {
			legacyRuns[h.RunID] = struct{}{}
		}
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		return report, err
	}
	defer func() { _ = st.Close() }()

	if err := diagnoseOrphanRuns(ctx, st, daemonLive, legacyRuns, dryRun, &report); err != nil {
		return report, err
	}
	if err := diagnoseLegacyBoxSlots(p, boxHolders, dryRun, &report); err != nil {
		return report, err
	}
	if err := diagnoseDeadConcurrency(ctx, st, dryRun, &report); err != nil {
		return report, err
	}
	if err := diagnoseDanglingRunDirs(ctx, st, p, dryRun, &report); err != nil {
		return report, err
	}
	if err := diagnosePoisonedProfiles(ctx, st, home, &report); err != nil {
		return report, err
	}
	diagnoseDaemonHealth(ctx, home, selfVersion, &report)
	diagnoseQuarantinedLedgers(home, &report)
	diagnoseStrayDaemons(ctx, home, &report)
	return report, nil
}

// scratchModuleVersion is the version a binary reports for the sparkwing
// SDK when its module requires the placeholder and points a replace
// directive at a local checkout. No release carries it and no ordinary
// project builds with it, so a daemon reporting it came from a scratch
// module -- in practice one a test scaffolded under a temp directory,
// ran, and left behind.
const scratchModuleVersion = "v0.0.0"

// diagnoseStrayDaemons probes this user's daemons for other sparkwing
// homes and reports any built from a scratch module. Such a daemon is
// invisible from this home, which reaches only its own socket, yet it
// stands beside the resident daemon in any process listing and is easily
// read as production state -- its log and its bind failures then explain
// outages it has nothing to do with. Read-only: doctor names it and
// leaves stopping it to the operator, who owns that process.
func diagnoseStrayDaemons(ctx context.Context, home string, report *DoctorReport) {
	socks, err := wingd.PeerSockets(home)
	if err != nil {
		return
	}
	for _, sock := range socks {
		info, err := wingdclient.Probe(ctx, sock)
		if err != nil || !scratchBuild(info.BinaryVersion) {
			continue
		}
		report.StrayDaemons = append(report.StrayDaemons,
			DoctorStrayDaemon{Socket: sock, Version: info.BinaryVersion})
	}
}

// scratchBuild reports whether a daemon's version marks it as a scratch
// build. A daemon on a release or a source build of the SDK is somebody's
// deliberate second home -- an isolated one is a documented remedy here --
// so only the placeholder counts.
func scratchBuild(version string) bool {
	return strings.TrimSpace(version) == scratchModuleVersion
}

// diagnosePoisonedProfiles scans the stored capacity rollups for profiles
// whose contended-run demand floor prices runs at or above the machine's
// grantable ceiling -- contention poisoning that otherwise surfaces only as
// a per-run warning while every run silently holds the whole box. Read-only:
// the remedy discards learned measurements, so it is named, not applied. The
// ceiling comes from the live daemon; with none running, the raw core count
// stands in (a higher bar, so absence of the daemon never over-flags).
func diagnosePoisonedProfiles(ctx context.Context, st *store.Store, home string, report *DoctorReport) error {
	profiles, err := st.ListPipelineProfiles(ctx, "")
	if err != nil {
		return err
	}
	grantable := grantableCores(ctx, home)
	for _, prof := range profiles {
		if prof.NodeID != "" || !capacity.FloorPoisoned(&prof, grantable) {
			continue
		}
		report.PoisonedProfiles = append(report.PoisonedProfiles, DoctorPoisonedProfile{
			Pipeline:       prof.Pipeline,
			FloorCores:     prof.FloorCores,
			ChargeCores:    capacity.SafetyMultiple * prof.FloorCores,
			GrantableCores: grantable,
		})
	}
	return nil
}

// grantableCores is the largest CPU charge the local daemon grants a single
// run on an idle box (capacity minus its reserve), else the machine's core
// count when no daemon answers.
func grantableCores(ctx context.Context, home string) float64 {
	qs, err := wingdclient.Query(ctx, wingdclient.Options{Home: home})
	if err == nil {
		for _, r := range qs.Resources {
			if r.Key == "cores" && r.Capacity > r.Reserved {
				return r.Capacity - r.Reserved
			}
		}
	}
	return float64(runtime.NumCPU())
}

// diagnoseQuarantinedLedgers reports admission state files the daemon
// quarantined after failing to restore them. They are evidence of a past
// bad shutdown or ledger defect, kept for inspection; doctor names them
// so they are found, and leaves removal to the operator.
func diagnoseQuarantinedLedgers(home string, report *DoctorReport) {
	dir, err := wingd.StateDir(home)
	if err != nil {
		return
	}
	matches, err := filepath.Glob(filepath.Join(dir, "state.json.corrupt-*"))
	if err != nil {
		return
	}
	report.QuarantinedLedgers = matches
}

// diagnoseDaemonHealth reads the resident daemon and reports the standing
// problems a fresh user on the happy path otherwise only sees as an opaque
// per-run failure: a repeated malformed-request rejection pattern (from the
// outcome window), a version skew between this binary and the daemon
// (takeover resolves a skew only when the client build supersedes the
// daemon's; otherwise the daemon stays and may reject requests it cannot
// honor), and the checkouts the daemon's protocol major locks out. It is
// read-only; an absent daemon yields nothing.
//
// Who the daemon is comes from the handshake ack, not from the queue state:
// a daemon speaking a protocol major this build does not refuses the queue
// read outright, and that refusal is the very incident the lockout scan
// exists to explain. A probe reads the ack before asking the daemon for
// anything, so it still learns which daemon is resident and what it speaks.
// The queue state is then read separately, for the checks that need it.
func diagnoseDaemonHealth(ctx context.Context, home, selfVersion string, report *DoctorReport) {
	sock, err := wingd.SocketPath(home)
	if err != nil {
		return
	}
	info, err := wingdclient.Probe(ctx, sock)
	if err != nil {
		return
	}
	if versionSkewed(selfVersion, info.BinaryVersion) {
		report.DaemonVersionSkew = &DoctorVersionSkew{Self: selfVersion, Daemon: info.BinaryVersion}
	}
	diagnoseLockedOutRepos(info.ProtocolMajor, info.BinaryVersion, wingwire.ReleasedProtocolFloors(), report)

	qs, err := wingdclient.Query(ctx, wingdclient.Options{Home: home})
	if err != nil || qs.Events == nil {
		return
	}
	for _, r := range qs.Events.Rejections {
		if r.Count >= doctorRejectionPatternThreshold {
			report.AdmissionRejections = append(report.AdmissionRejections,
				DoctorRejection{Cause: r.Cause, Count: r.Count})
		}
	}
}

// diagnoseLockedOutRepos names the registered repos whose pinned SDK sits
// below the release that speaks to the resident daemon, so it refuses them.
//
// This is the skew that actually stops work, and it is invisible from inside
// the repo that hits it: the daemon is machine-wide and the first run needing
// one brings it up, so a single current repo can lock out every older one and
// the error surfaces in the victim rather than the cause. Read-only -- the
// remedy edits another repo's go.mod, which is the operator's call.
//
// daemonMajor is what the daemon said in its handshake ack, never what its
// version implies: a table can place a release only below its newest row, so
// reading a major off a version quietly reports a floor and calls a daemon
// past that row current. Each pin is then placed by one comparison against
// raiseTo, the lowest release known to speak daemonMajor -- the table's
// majors ascend with their floors, so a pin below that release speaks an
// older major and a pin at or above it does not.
//
// When the table ends below daemonMajor this build cannot name that release:
// which release first spoke a major was decided after this build was cut. The
// daemon is running one that speaks it, so its own version stands in as a
// target that provably works, and the gap is reported alongside
// ([DoctorReport.DaemonProtocolGap]) so a floor is never read as exact. The
// set of repos then errs wide rather than silent: a pin between the true
// floor and the daemon's release is named although it may already be high
// enough, which costs an operator a pin bump they did not need, against a
// silence that costs them the whole diagnosis. A pin that will not parse is
// not evidence against anyone; neither is a daemon version that will not parse,
// names a scratch build, or is a prerelease -- a prerelease sorts below the
// release it names, which would clear pins at that release rather than flag
// them, and a prerelease cannot be written into go.mod as a target. Repos
// whose SDK is supplied by a replace directive or a go.work `use` are
// exonerated last, after the pin has already been found wanting -- those
// build the pipeline from a local checkout, so the declared pin describes
// nothing that runs and naming it would send an operator to edit a line that
// changes no behavior.
func diagnoseLockedOutRepos(daemonMajor int, daemonVersion string, floors wingwire.ProtocolFloors, report *DoctorReport) {
	newest, covered := floors.Newest()
	if !covered || daemonMajor <= 0 {
		return
	}
	raiseTo, known := floors.MinVersionSpeaking(daemonMajor)
	if !known {
		report.DaemonProtocolGap = &DoctorProtocolGap{
			Self:          newest.Major,
			Daemon:        daemonMajor,
			DaemonVersion: daemonVersion,
		}
		if !semver.IsValid(daemonVersion) || scratchBuild(daemonVersion) || semver.Prerelease(daemonVersion) != "" {
			return
		}
		raiseTo = daemonVersion
	}
	cands, err := repos.CandidatePaths()
	if err != nil {
		return
	}
	for _, c := range cands {
		sparkwingDir := filepath.Join(c.Path, ".sparkwing")
		pin, replace := repos.SDKPin(sparkwingDir)
		if pin == "" || replace != "" || !semver.IsValid(pin) {
			continue
		}
		if semver.Compare(pin, raiseTo) >= 0 {
			continue
		}
		if repos.SDKWorkspaceOverride(sparkwingDir) != "" {
			continue
		}
		report.LockedOutRepos = append(report.LockedOutRepos, DoctorLockedOutRepo{
			Name:     filepath.Base(c.Path),
			Path:     c.Path,
			Pin:      pin,
			RaiseTo:  raiseTo,
			Worktree: c.Worktree,
		})
	}
	sort.Slice(report.LockedOutRepos, func(i, j int) bool {
		return report.LockedOutRepos[i].Path < report.LockedOutRepos[j].Path
	})
}

// versionSkewed reports whether the running binary and the live daemon are
// provably different builds. Empty or unknown versions on either side are not
// a provable skew, so they never flag.
func versionSkewed(self, daemon string) bool {
	if self == "" || daemon == "" || self == "(unknown)" || daemon == "(unknown)" {
		return false
	}
	return self != daemon
}

// rejectionExplanation renders the human cause and recommended action for a
// repeated admission-rejection cause.
func rejectionExplanation(cause string) string {
	switch cause {
	case "cost_source":
		return "runs named a cost source this box's daemon does not recognize (the launching sparkwing is newer than the resident daemon); align the two builds, or pin resources explicitly with plan.Resources(sparkwing.Cores(n), sparkwing.MemoryGB(n))"
	case "request":
		return "runs submitted a malformed admission request (the daemon log names the offending input); usually a version skew between the run and the daemon"
	default:
		return "the daemon log names the offending input for each"
	}
}

// liveDaemonRuns returns the set of run ids the local admission daemon is
// holding or queueing, so orphan detection never finalizes a run the daemon
// still tracks. An absent daemon means no live leases, so the set is empty.
func liveDaemonRuns(ctx context.Context, home string) map[string]struct{} {
	live := map[string]struct{}{}
	qs, err := wingdclient.Query(ctx, wingdclient.Options{Home: home})
	if err != nil {
		return live
	}
	for _, h := range qs.Holders {
		live[h.RunID] = struct{}{}
	}
	for _, w := range qs.Waiters {
		live[w.RunID] = struct{}{}
	}
	return live
}

func diagnoseOrphanRuns(ctx context.Context, st *store.Store, daemonLive, legacyRuns map[string]struct{}, dryRun bool, report *DoctorReport) error {
	running, err := st.ListRuns(ctx, store.RunFilter{Statuses: []string{"running"}, Limit: 1000})
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-doctorRunOrphanGrace)
	for _, r := range running {
		if _, ok := daemonLive[r.ID]; ok {
			continue
		}
		if _, ok := legacyRuns[r.ID]; ok {
			continue
		}
		anchor := r.StartedAt
		if r.LastHeartbeatAt != nil {
			anchor = *r.LastHeartbeatAt
		}
		if anchor.IsZero() || !anchor.Before(cutoff) {
			continue
		}
		report.OrphanedRuns = append(report.OrphanedRuns, r.ID)
		if dryRun {
			continue
		}
		if err := st.FinishRun(ctx, r.ID, "cancelled",
			"interrupted: no live process or daemon lease (finalized by sparkwing doctor)"); err != nil {
			return err
		}
	}
	return nil
}

func diagnoseLegacyBoxSlots(p paths.Paths, holders []boxslot.Holder, dryRun bool, report *DoctorReport) error {
	dir := p.BoxSlotDir()
	for _, h := range holders {
		if h.Live {
			report.LiveLegacyHolders = append(report.LiveLegacyHolders, DoctorLegacyHolder{
				PID: h.PID, RunID: h.RunID, Lock: h.Path,
			})
		}
	}
	if len(report.LiveLegacyHolders) > 0 {
		return nil
	}
	if dryRun {
		n, err := countDirFiles(dir)
		if err != nil {
			return err
		}
		report.LegacyBoxSlotFilesRemoved = n
		return nil
	}
	removed, live, err := boxslot.PurgeIfIdle(dir)
	if err != nil {
		return err
	}
	if len(live) > 0 {
		return nil
	}
	report.LegacyBoxSlotFilesRemoved = removed
	return nil
}

func countDirFiles(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return n, nil
}

func diagnoseDeadConcurrency(ctx context.Context, st *store.Store, dryRun bool, report *DoctorReport) error {
	if dryRun {
		h, w, err := st.CountDeadLocalConcurrency(ctx)
		if err != nil {
			return err
		}
		report.DeadConcurrencyHolders, report.DeadConcurrencyWaiters = h, w
		return nil
	}
	h, w, err := st.PurgeDeadLocalConcurrency(ctx)
	if err != nil {
		return err
	}
	report.DeadConcurrencyHolders, report.DeadConcurrencyWaiters = h, w
	return nil
}

func diagnoseDanglingRunDirs(ctx context.Context, st *store.Store, p paths.Paths, dryRun bool, report *DoctorReport) error {
	entries, err := os.ReadDir(p.RunsDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		_, err := st.GetRun(ctx, e.Name())
		if err == nil {
			continue
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		report.DanglingRunDirs = append(report.DanglingRunDirs, e.Name())
		if dryRun {
			continue
		}
		if err := os.RemoveAll(filepath.Join(p.RunsDir(), e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// RenderDoctor writes r in the requested format: "json", "plain", or pretty.
// legacyLine, when non-empty, is the legacy-coexistence warning appended to
// the pretty view (the caller owns the legacy-count phrasing).
func RenderDoctor(w io.Writer, r DoctorReport, format, legacyLine string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case "plain":
		return renderDoctorPlain(w, r)
	default:
		return renderDoctorPretty(w, r, legacyLine)
	}
}

func renderDoctorPlain(w io.Writer, r DoctorReport) error {
	fmt.Fprintf(w, "orphaned_runs\t%d\n", len(r.OrphanedRuns))
	fmt.Fprintf(w, "legacy_box_slot_files_removed\t%d\n", r.LegacyBoxSlotFilesRemoved)
	fmt.Fprintf(w, "live_legacy_holders\t%d\n", len(r.LiveLegacyHolders))
	fmt.Fprintf(w, "dead_concurrency_holders\t%d\n", r.DeadConcurrencyHolders)
	fmt.Fprintf(w, "dead_concurrency_waiters\t%d\n", r.DeadConcurrencyWaiters)
	fmt.Fprintf(w, "dangling_run_dirs\t%d\n", len(r.DanglingRunDirs))
	rejections := 0
	for _, rej := range r.AdmissionRejections {
		rejections += rej.Count
	}
	fmt.Fprintf(w, "admission_rejections\t%d\n", rejections)
	skew := 0
	if r.DaemonVersionSkew != nil {
		skew = 1
	}
	fmt.Fprintf(w, "daemon_version_skew\t%d\n", skew)
	fmt.Fprintf(w, "locked_out_repos\t%d\n", len(r.LockedOutRepos))
	protocolGap := 0
	if r.DaemonProtocolGap != nil {
		protocolGap = 1
	}
	fmt.Fprintf(w, "daemon_protocol_gap\t%d\n", protocolGap)
	fmt.Fprintf(w, "quarantined_ledgers\t%d\n", len(r.QuarantinedLedgers))
	fmt.Fprintf(w, "poisoned_profiles\t%d\n", len(r.PoisonedProfiles))
	shadowed := 0
	if r.ShadowedHooks != nil {
		shadowed = len(r.ShadowedHooks.Gates)
	}
	fmt.Fprintf(w, "shadowed_hooks\t%d\n", shadowed)
	fmt.Fprintf(w, "ungated_repos\t%d\n", len(r.UngatedRepos))
	fmt.Fprintf(w, "stray_daemons\t%d\n", len(r.StrayDaemons))
	return nil
}

func renderDoctorPretty(w io.Writer, r DoctorReport, legacyLine string) error {
	verb, would := "removed", ""
	if r.DryRun {
		verb, would = "found", " (dry run: nothing changed)"
	}
	if r.Clean() {
		fmt.Fprintf(w, "healthy: nothing to repair%s\n", would)
		renderUngatedRepos(w, r)
		renderStrayDaemons(w, r)
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if n := len(r.OrphanedRuns); n > 0 {
		fmt.Fprintf(tw, "orphaned runs finalized\t%d\n", n)
	}
	if r.LegacyBoxSlotFilesRemoved > 0 {
		fmt.Fprintf(tw, "legacy box-slot files %s\t%d\n", verb, r.LegacyBoxSlotFilesRemoved)
	}
	if r.DeadConcurrencyHolders > 0 || r.DeadConcurrencyWaiters > 0 {
		fmt.Fprintf(tw, "dead local concurrency rows %s\t%d holders, %d waiters\n",
			verb, r.DeadConcurrencyHolders, r.DeadConcurrencyWaiters)
	}
	if n := len(r.DanglingRunDirs); n > 0 {
		fmt.Fprintf(tw, "dangling run directories %s\t%d\n", verb, n)
	}
	_ = tw.Flush()

	for _, rej := range r.AdmissionRejections {
		fmt.Fprintf(w, "\nwarning: %d admission request(s) rejected as invalid (%s)\n  %s\n",
			rej.Count, rej.Cause, rejectionExplanation(rej.Cause))
	}
	if s := r.DaemonVersionSkew; s != nil {
		fmt.Fprintf(w, "\nwarning: version skew -- this sparkwing is %s but the running admission daemon is %s\n  a client build that supersedes the daemon (a newer release, or a build from source) takes it over on its next run; when the daemon is the newer or source-built side, requests it cannot honor fail as invalid -- stop the daemon so the next run brings up a matching one, or run in an isolated SPARKWING_HOME\n",
			s.Self, s.Daemon)
	}

	renderLockedOutRepos(w, r)

	if n := len(r.QuarantinedLedgers); n > 0 {
		fmt.Fprintf(w, "\nwarning: %d quarantined admission ledger file(s) -- the daemon could not restore them and started fresh\n", n)
		for _, f := range r.QuarantinedLedgers {
			fmt.Fprintf(w, "  %s\n", f)
		}
		fmt.Fprintf(w, "  kept for inspection; safe to delete once reviewed\n")
	}

	if s := r.ShadowedHooks; s != nil {
		fmt.Fprintf(w, "\nwarning: %s\n  %s\n", s.Summary(), s.Remedy())
	}

	renderUngatedRepos(w, r)

	for _, p := range r.PoisonedProfiles {
		fmt.Fprintf(w, "\nwarning: capacity profile %q looks poisoned by contention -- its demand floor %.1f cores prices runs at %.1f, at or over the grantable %.1f, so every run holds the whole machine\n  reset it: sparkwing runs stats --reset --pipeline %s\n",
			p.Pipeline, p.FloorCores, p.ChargeCores, p.GrantableCores, p.Pipeline)
	}

	if legacyLine != "" {
		fmt.Fprintf(w, "\nwarning: %s\n", legacyLine)
		for _, h := range r.LiveLegacyHolders {
			fmt.Fprintf(w, "  pid %d holding %s\n", h.PID, h.Lock)
		}
	}
	renderStrayDaemons(w, r)
	return nil
}

// renderLockedOutRepos writes the checkouts the resident daemon refuses, each
// with the release its own pin has to reach, and then the protocol gap when
// this build is the older side of the handshake as well.
//
// Which lever moves is the whole point of the warning, and it is not the same
// lever in both states. With the daemon's protocol known, the CLI is a
// bystander to the refusal and upgrading it changes nothing. With the daemon
// past this build's table, the CLI is refused too and carries the stale table
// the targets were read from, so updating it is exactly what sharpens the
// answer -- printing the bystander sentence there would argue against the one
// action that helps.
func renderLockedOutRepos(w io.Writer, r DoctorReport) {
	if n := len(r.LockedOutRepos); n > 0 {
		fmt.Fprintf(w, "\nwarning: %d checkout(s) cannot gate against the resident admission daemon -- their pinned SDK is below the release that speaks to it\n", n)
		rows := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, lr := range r.LockedOutRepos {
			kind := ""
			if lr.Worktree {
				kind = " (worktree)"
			}
			fmt.Fprintf(rows, "  %s%s\tpinned %s\traise to %s\t%s\n", lr.Name, kind, lr.Pin, lr.RaiseTo, lr.Path)
		}
		_ = rows.Flush()
		fmt.Fprint(w, "  every pipeline run in these checkouts is refused until the pin in that checkout's .sparkwing/go.mod reaches the release named on its row")
		if r.DaemonProtocolGap == nil {
			fmt.Fprint(w, "; the sparkwing CLI is not the client here and upgrading it does not help")
		} else {
			fmt.Fprint(w, ", which is the daemon's own release -- this build cannot name the lowest release that would do")
		}
		fmt.Fprintln(w)
	}
	g := r.DaemonProtocolGap
	if g == nil {
		return
	}
	daemon := ""
	if g.DaemonVersion != "" {
		daemon = " (sparkwing " + g.DaemonVersion + ")"
	}
	fmt.Fprintf(w, "\nwarning: the resident admission daemon%s speaks wire protocol %d and this sparkwing speaks %d\n",
		daemon, g.Daemon, g.Self)
	fmt.Fprintf(w, "  that daemon refuses this CLI too, and the release table this build carries stops below protocol %d, so it cannot name which release first spoke it -- update the sparkwing CLI and re-run doctor for an exact answer\n",
		g.Daemon)
}

// renderUngatedRepos writes the fleet-wide ungated warning, skipping the
// checkout ShadowedHooks already described in full so one repository is not
// reported twice under two headings.
func renderUngatedRepos(w io.Writer, r DoctorReport) {
	rows := r.UngatedRepos
	if s := r.ShadowedHooks; s != nil {
		rows = nil
		for _, g := range r.UngatedRepos {
			if !githooks.SameDir(g.Repo, s.Repo) {
				rows = append(rows, g)
			}
		}
	}
	if len(rows) == 0 {
		return
	}
	fmt.Fprintf(w, "\nwarning: %d registered repo(s) accept commits with no gate\n", len(rows))
	for _, g := range rows {
		fmt.Fprintf(w, "  %s\n    %s\n", g.Summary(), g.Remedy())
	}
}

// renderStrayDaemons writes the stray-daemon warnings. It runs on both the
// healthy and the unhealthy path, since a stray belongs to no home and a
// home with nothing to repair is exactly where one goes unnoticed.
func renderStrayDaemons(w io.Writer, r DoctorReport) {
	for _, d := range r.StrayDaemons {
		fmt.Fprintf(w, "\nwarning: an admission daemon for another sparkwing home reports version %s, which no release carries\n"+
			"  %s is what a binary built from a module that replaces the sparkwing SDK with a local checkout reports, so this daemon came from a scratch module -- typically one a test scaffolded under a temp directory and left running, whose process arguments still name that temp path\n"+
			"  it is not this machine's resident daemon: its log, its bind failures, and its queue describe nothing in production. Stop that process before diagnosing anything from it\n"+
			"  socket: %s\n", d.Version, d.Version, d.Socket)
	}
}
