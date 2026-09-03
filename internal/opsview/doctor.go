package opsview

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
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
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/githooks"
	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const doctorRunOrphanGrace = 2 * time.Minute

const doctorRejectionPatternThreshold = 3

type DoctorReport struct {
	DryRun bool `json:"dry_run"`

	PermissionRepairs []fssecure.Change `json:"permission_repairs,omitempty"`

	PermissionAuditUnverified bool `json:"permission_audit_unverified,omitempty"`

	OrphanedRuns []string `json:"orphaned_runs,omitempty"`

	LegacyBoxSlotFilesRemoved int `json:"legacy_box_slot_files_removed"`

	LiveLegacyHolders []DoctorLegacyHolder `json:"live_legacy_holders,omitempty"`

	DeadConcurrencyHolders int `json:"dead_concurrency_holders"`
	DeadConcurrencyWaiters int `json:"dead_concurrency_waiters"`

	DanglingRunDirs []string `json:"dangling_run_dirs,omitempty"`

	AdmissionRejections []DoctorRejection `json:"admission_rejections,omitempty"`

	DaemonVersionSkew *DoctorVersionSkew `json:"daemon_version_skew,omitempty"`

	LockedOutRepos []DoctorLockedOutRepo `json:"locked_out_repos,omitempty"`

	DaemonProtocolGap *DoctorProtocolGap `json:"daemon_protocol_gap,omitempty"`

	QuarantinedLedgers []string `json:"quarantined_ledgers,omitempty"`

	PoisonedProfiles []DoctorPoisonedProfile `json:"poisoned_profiles,omitempty"`

	ShadowedHooks *githooks.Shadow `json:"shadowed_hooks,omitempty"`

	UngatedRepos []githooks.RepoGates `json:"ungated_repos,omitempty"`

	GatesSurveyed int `json:"gates_surveyed"`

	GatesSurveyError string `json:"gates_survey_error,omitempty"`

	MachineBudget *DoctorMachineBudget `json:"machine_budget,omitempty"`

	StrayDaemons []DoctorStrayDaemon `json:"stray_daemons,omitempty"`

	InstallConflict *DoctorInstallConflict `json:"install_conflict,omitempty"`

	Daemon DoctorDaemon `json:"daemon"`

	Toolchains []DoctorToolchain `json:"toolchains,omitempty"`

	StandaloneStores []DoctorStandaloneStore `json:"standalone_stores,omitempty"`
}

// DoctorStandaloneStore is one runs store written by pipeline binaries that
// could not reach the admission daemon. Its runs are invisible to
// `sparkwing runs` and to the dashboard, which read the shared store.
type DoctorStandaloneStore struct {
	Path string `json:"path"`

	// Schema is 0 for the store binaries share under the requirements rule,
	// and the schema version of the binaries that keep their own otherwise.
	Schema int `json:"schema"`

	Runs int `json:"runs"`

	OldestRunAt *time.Time `json:"oldest_run_at,omitempty"`
}

// DoctorToolchain is one CLI release in the version store that a repo's SDK pin
// can select.
type DoctorToolchain struct {
	Version string `json:"version"`

	Path string `json:"path"`

	Bytes int64 `json:"bytes"`
}

type DoctorDaemon struct {
	Reachable bool `json:"reachable"`

	State string `json:"state"`

	Socket string `json:"socket,omitempty"`

	// APISocket is where the daemon serves the controller HTTP API.
	APISocket string `json:"api_socket,omitempty"`

	// APIReady reports whether that socket is bound and serving. Nil from a
	// daemon that does not report the field, which is how "not serving" is
	// distinguishable from "cannot say".
	APIReady *bool `json:"api_ready,omitempty"`

	// APIError is why that socket is unbound.
	APIError string `json:"api_error,omitempty"`

	Version string `json:"version,omitempty"`

	ProtocolMajor int `json:"protocol_major,omitempty"`

	Detail string `json:"detail,omitempty"`
}

func (d DoctorDaemon) Blind() bool { return d.State == ReachUnreachable }

// APIUnserved reports a serving daemon whose controller API socket is
// unbound, so no run it hosts can reach its state. A daemon that does not
// report the field cannot be judged on it.
func (d DoctorDaemon) APIUnserved() bool {
	return d.State == ReachServing && d.APIReady != nil && !*d.APIReady
}

type DoctorInstallConflict struct {
	Self string `json:"self"`

	Competing []DoctorInstallCopy `json:"competing"`
}

type DoctorInstallCopy struct {
	Path     string `json:"path"`
	Resolved string `json:"resolved,omitempty"`

	Modified string `json:"modified,omitempty"`
}

type DoctorMachineBudget struct {
	Source string `json:"source"`

	Origin string `json:"origin,omitempty"`

	Raw string `json:"raw,omitempty"`

	Cores        float64 `json:"cores,omitempty"`
	MachineCores float64 `json:"machine_cores,omitempty"`

	MemoryBytes        int64 `json:"memory_bytes,omitempty"`
	MachineMemoryBytes int64 `json:"machine_memory_bytes,omitempty"`

	Enforce bool `json:"enforce,omitempty"`

	IgnoreExternal bool `json:"ignore_external,omitempty"`
}

type DoctorStrayDaemon struct {
	Socket string `json:"socket"`

	Version string `json:"version"`
}

type DoctorPoisonedProfile struct {
	Pipeline       string  `json:"pipeline"`
	FloorCores     float64 `json:"floor_cores"`
	ChargeCores    float64 `json:"charge_cores"`
	GrantableCores float64 `json:"grantable_cores"`
}

type DoctorRejection struct {
	Cause string `json:"cause"`
	Count int    `json:"count"`
}

type DoctorVersionSkew struct {
	Self string `json:"self"`

	Daemon string `json:"daemon"`
}

type DoctorLockedOutRepo struct {
	Name string `json:"name"`

	Path string `json:"path"`

	Worktree bool `json:"worktree,omitempty"`

	Pin string `json:"pin"`

	RaiseTo string `json:"raise_to"`
}

type DoctorProtocolGap struct {
	Self int `json:"self"`

	Daemon int `json:"daemon"`

	DaemonVersion string `json:"daemon_version"`
}

type DoctorLegacyHolder struct {
	PID   int    `json:"pid"`
	RunID string `json:"run_id,omitempty"`
	Lock  string `json:"lock"`
}

func (r DoctorReport) Clean() bool {
	return !r.Daemon.Blind() &&
		!r.Daemon.APIUnserved() &&
		len(r.PermissionRepairs) == 0 &&
		!r.PermissionAuditUnverified &&
		len(r.OrphanedRuns) == 0 &&
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
		r.InstallConflict == nil &&
		r.ShadowedHooks == nil
}

func Diagnose(ctx context.Context, p paths.Paths, home, selfVersion string, dryRun bool) (DoctorReport, error) {
	report := DoctorReport{DryRun: dryRun}
	if err := validateDoctorRoot(p, home); err != nil {
		return report, err
	}
	home = p.Root
	diagnoseToolchains(p, &report)
	diagnoseStandaloneStores(p, &report)
	if err := validateDoctorMutationPaths(p, nil, false); err != nil {
		return report, err
	}
	rootIdentity, repairable, err := recognizedSparkwingHome(p)
	if err != nil {
		return report, err
	}
	if !repairable {
		return report, fmt.Errorf("refuse doctor mutation for unrecognized sparkwing home %q", p.Root)
	}
	if fssecure.AuditSupported() {
		var repairs []fssecure.Change
		if rootIdentity != nil {
			repairs, err = fssecure.RepairTree(p.Root, rootIdentity, dryRun)
		}
		report.PermissionRepairs = repairs
		if err != nil {
			return report, fmt.Errorf("repair private-home permissions after reporting %d changed path(s): %w", len(repairs), err)
		}
	} else {
		report.PermissionAuditUnverified = true
	}
	if !dryRun {
		if err := validateDoctorMutationPaths(p, rootIdentity, rootIdentity == nil); err != nil {
			return report, err
		}
	}
	homeRoot, err := openDoctorHomeRoot(p.Root, rootIdentity)
	if err != nil {
		return report, err
	}
	if homeRoot == nil {
		report.Daemon = probeDaemon(ctx, home)
		diagnoseInstallConflict(&report)
		return report, nil
	}
	defer func() { _ = homeRoot.Close() }()
	boxRoot, _, err := openDoctorChildRoot(homeRoot, "box-slots")
	if err != nil {
		return report, err
	}
	if boxRoot != nil {
		defer func() { _ = boxRoot.Close() }()
	}
	runsRoot, _, err := openDoctorChildRoot(homeRoot, "runs")
	if err != nil {
		return report, err
	}
	if runsRoot != nil {
		defer func() { _ = runsRoot.Close() }()
	}
	st, stateFile, err := openDoctorState(p, homeRoot, rootIdentity, dryRun)
	if err != nil {
		return report, err
	}
	if stateFile != nil {
		defer stateFile.Close()
	}
	if st != nil {
		defer func() { _ = st.Close() }()
	}

	report.Daemon = probeDaemon(ctx, home)
	queueState, queueRead := probeDaemonQueue(ctx, &report)
	daemonLive := liveDaemonRuns(queueState, queueRead)

	var boxHolders []boxslot.Holder
	if boxRoot != nil {
		boxHolders, err = boxslot.HoldersInRoot(boxRoot, p.BoxSlotDir())
		if err != nil {
			return report, err
		}
	}
	legacyRuns := map[string]struct{}{}
	for _, h := range boxHolders {
		if h.Live && h.RunID != "" {
			legacyRuns[h.RunID] = struct{}{}
		}
	}

	if err := diagnoseLegacyBoxSlots(boxRoot, p.BoxSlotDir(), boxHolders, dryRun, &report); err != nil {
		return report, err
	}
	if st != nil {
		if err := diagnoseOrphanRuns(ctx, st, daemonLive, legacyRuns, report.Daemon.Blind(), dryRun, &report); err != nil {
			return report, err
		}
		if err := diagnoseDeadConcurrency(ctx, st, dryRun, &report); err != nil {
			return report, err
		}
		if err := diagnoseDanglingRunDirs(ctx, st, runsRoot, dryRun, &report); err != nil {
			return report, err
		}
		if err := diagnosePoisonedProfiles(ctx, st, queueState, queueRead, &report); err != nil {
			return report, err
		}
	}
	diagnoseDaemonHealth(selfVersion, queueState, queueRead, &report)
	diagnoseQuarantinedLedgers(home, &report)
	diagnoseStrayDaemons(ctx, home, &report)
	diagnoseInstallConflict(&report)
	return report, nil
}

func openDoctorHomeRoot(path string, expected os.FileInfo) (*os.Root, error) {
	if expected == nil {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("refuse doctor mutation because missing home root %q appeared after recognition", path)
		}
		return nil, nil
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(expected, opened) {
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("refuse doctor mutation because home root %q changed after recognition", path)
	}
	return root, nil
}

func openDoctorChildRoot(root *os.Root, name string) (*os.Root, os.FileInfo, error) {
	info, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("refuse doctor mutation through symlink %q", name)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("doctor path %q is not a directory", name)
	}
	child, err := root.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	opened, err := child.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = child.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("refuse doctor mutation because %q changed while opening", name)
	}
	return child, info, nil
}

func openDoctorState(p paths.Paths, homeRoot *os.Root, rootIdentity os.FileInfo, dryRun bool) (*store.Store, *os.File, error) {
	info, err := homeRoot.Lstat("state.db")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("refuse doctor mutation because state.db is not a regular file")
	}
	stateFile, err := homeRoot.OpenFile("state.db", os.O_RDONLY, 0)
	if err != nil {
		return nil, nil, err
	}
	opened, err := stateFile.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = stateFile.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("refuse doctor mutation because state.db changed while opening")
	}
	if err := validatePinnedDoctorRootPath(p.Root, rootIdentity); err != nil {
		_ = stateFile.Close()
		return nil, nil, err
	}
	var st *store.Store
	if dryRun {
		st, err = store.OpenReadOnlySnapshot(p.StateDB())
	} else {
		st, err = store.Open(p.StateDB())
	}
	if err != nil {
		_ = stateFile.Close()
		return nil, nil, err
	}
	after, afterErr := homeRoot.Lstat("state.db")
	rootErr := validatePinnedDoctorRootPath(p.Root, rootIdentity)
	if afterErr != nil || !os.SameFile(info, after) || rootErr != nil {
		_ = st.Close()
		_ = stateFile.Close()
		if afterErr != nil {
			return nil, nil, afterErr
		}
		if rootErr != nil {
			return nil, nil, rootErr
		}
		return nil, nil, fmt.Errorf("refuse doctor mutation because state.db changed while opening store")
	}
	return st, stateFile, nil
}

func validatePinnedDoctorRootPath(path string, expected os.FileInfo) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected, info) {
		return fmt.Errorf("refuse doctor mutation because home root %q changed after recognition", path)
	}
	return nil
}

func validateDoctorRoot(p paths.Paths, home string) error {
	if p.Root == "" {
		return errors.New("doctor: Sparkwing home root is empty")
	}
	if home == "" {
		return nil
	}
	pathsRoot, err := filepath.Abs(p.Root)
	if err != nil {
		return err
	}
	homeRoot, err := filepath.Abs(home)
	if err != nil {
		return err
	}
	if filepath.Clean(pathsRoot) == filepath.Clean(homeRoot) {
		return nil
	}
	pathsInfo, pathsErr := os.Stat(pathsRoot)
	homeInfo, homeErr := os.Stat(homeRoot)
	if pathsErr == nil && homeErr == nil && os.SameFile(pathsInfo, homeInfo) {
		return nil
	}
	return fmt.Errorf("doctor paths root %q does not identify requested home %q", p.Root, home)
}

func validateDoctorMutationPaths(p paths.Paths, expectedRoot os.FileInfo, expectMissing bool) error {
	rootInfo, err := os.Lstat(p.Root)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		if expectMissing {
			return fmt.Errorf("refuse doctor mutation because missing home root %q appeared after recognition", p.Root)
		}
		if rootInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse doctor mutation through symlink root %q", p.Root)
		}
		if expectedRoot != nil && !os.SameFile(expectedRoot, rootInfo) {
			return fmt.Errorf("refuse doctor mutation because home root %q changed after recognition", p.Root)
		}
	}
	paths := []string{
		p.RunsDir(),
		p.BoxSlotDir(),
		p.StateDB(),
		p.StateDB() + "-wal",
		p.StateDB() + "-shm",
		p.StateDB() + "-journal",
	}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse doctor mutation through symlink %q", path)
		}
	}
	return nil
}

func recognizedSparkwingHome(p paths.Paths) (os.FileInfo, bool, error) {
	rootInfo, err := os.Lstat(p.Root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, true, nil
		}
		return nil, false, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("refuse permission repair through symlink root %q", p.Root)
	}
	if !rootInfo.IsDir() {
		return nil, false, fmt.Errorf("sparkwing home %q is not a directory", p.Root)
	}
	entries, err := os.ReadDir(p.Root)
	if err != nil {
		return nil, false, err
	}
	if len(entries) == 0 {
		return rootInfo, true, nil
	}
	if sqliteHasTables(p.StateDB(), "runs", "nodes", "events", "triggers") ||
		sqliteHasTables(filepath.Join(p.Root, "outbox.db"), "outbox_writes") ||
		validWingdState(filepath.Join(p.Root, "wingd", "state.json")) ||
		hasValidVersionStamp(p.VersionStampDir()) {
		return rootInfo, true, nil
	}
	return rootInfo, false, nil
}

func hasValidVersionStamp(root string) bool {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || len(entry.Name()) != 16 {
			continue
		}
		if _, err := hex.DecodeString(entry.Name()); err != nil {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil || len(body) > 64*1024 {
			continue
		}
		lines := strings.Split(string(body), "\n")
		if len(lines) >= 2 && strings.HasPrefix(lines[0], "# ") && filepath.IsAbs(strings.TrimSpace(strings.TrimPrefix(lines[0], "# "))) && strings.TrimSpace(lines[1]) != "" {
			return true
		}
	}
	return false
}

func validWingdState(path string) bool {
	dirInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return false
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var state struct {
		Schema   int             `json:"schema"`
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		return false
	}
	snapshot := bytes.TrimSpace(state.Snapshot)
	return state.Schema > 0 &&
		len(snapshot) >= 2 && snapshot[0] == '{' && snapshot[len(snapshot)-1] == '}'
}

func sqliteHasTables(path string, names ...string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	st, err := store.OpenReadOnlySnapshot(path)
	if err != nil {
		return false
	}
	defer func() { _ = st.Close() }()
	for _, name := range names {
		var count int
		if err := st.DB().QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name,
		).Scan(&count); err != nil || count != 1 {
			return false
		}
	}
	return true
}

func diagnoseToolchains(p paths.Paths, report *DoctorReport) {
	entries, err := os.ReadDir(p.ToolchainsDir())
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		version := entry.Name()
		binary := p.ToolchainBinary(version)
		info, err := os.Stat(binary)
		if err != nil {
			continue
		}
		report.Toolchains = append(report.Toolchains, DoctorToolchain{
			Version: version,
			Path:    binary,
			Bytes:   info.Size(),
		})
	}
	sort.Slice(report.Toolchains, func(i, j int) bool {
		return report.Toolchains[i].Version < report.Toolchains[j].Version
	})
}

func diagnoseStandaloneStores(p paths.Paths, report *DoctorReport) {
	for _, s := range p.StandaloneStores() {
		addStandaloneStore(report, s.Path, s.Schema)
	}
}

func addStandaloneStore(report *DoctorReport, path string, schema int) {
	runs, oldest, ok := standaloneRunCount(path)
	if !ok {
		return
	}
	report.StandaloneStores = append(report.StandaloneStores, DoctorStandaloneStore{
		Path: path, Schema: schema, Runs: runs, OldestRunAt: oldest,
	})
}

// safety: read-only and without migration, because a store at another
// schema belongs to a binary that must keep opening it; doctor reporting on
// one must never be what ratchets it out of that binary's reach.
func standaloneRunCount(path string) (int, *time.Time, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0, nil, false
	}
	st, err := store.OpenReadOnly(path)
	if err != nil {
		return -1, nil, true
	}
	defer func() { _ = st.Close() }()
	var runs int
	var oldest sql.NullInt64
	if err := st.DB().QueryRow(`SELECT COUNT(*), MIN(started_at) FROM runs`).Scan(&runs, &oldest); err != nil {
		return -1, nil, true
	}
	if !oldest.Valid {
		return runs, nil, true
	}
	at := time.Unix(0, oldest.Int64).UTC()
	return runs, &at, true
}

func diagnoseInstallConflict(report *DoctorReport) {
	if paths.UnderTest() {
		return
	}
	self, err := installsite.Self()
	if err != nil {
		return
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		userHome = ""
	}
	report.InstallConflict = InstallConflict(self, installsite.SearchDirs(os.Getenv, userHome))
}

func InstallConflict(self string, dirs []string) *DoctorInstallConflict {
	competing := installsite.Competing(installsite.Scan(dirs), self)
	if len(competing) == 0 {
		return nil
	}
	sort.Slice(competing, func(i, j int) bool {
		return competing[i].ModTime.After(competing[j].ModTime)
	})
	out := &DoctorInstallConflict{Self: self}
	for _, c := range competing {
		row := DoctorInstallCopy{Path: c.Path, Modified: c.ModTime.UTC().Format(time.RFC3339)}
		if c.Resolved != c.Path {
			row.Resolved = c.Resolved
		}
		out.Competing = append(out.Competing, row)
	}
	return out
}

const scratchModuleVersion = "v0.0.0"

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

func scratchBuild(version string) bool {
	return strings.TrimSpace(version) == scratchModuleVersion
}

func diagnosePoisonedProfiles(ctx context.Context, st *store.Store, queueState wingwire.QueueState, queueRead bool, report *DoctorReport) error {
	profiles, err := st.ListPipelineProfiles(ctx, "")
	if err != nil {
		return err
	}
	grantable := grantableCores(queueState, queueRead)
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

func grantableCores(queueState wingwire.QueueState, queueRead bool) float64 {
	if queueRead {
		for _, r := range queueState.Resources {
			if r.Key == "cores" && r.Capacity > r.Reserved {
				return r.Capacity - r.Reserved
			}
		}
	}
	return float64(runtime.NumCPU())
}

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

func probeDaemon(ctx context.Context, home string) DoctorDaemon {
	sock, err := wingd.SocketPath(home)
	if err != nil {
		return DoctorDaemon{State: ReachUnreachable, Detail: "could not resolve the daemon socket path: " + err.Error()}
	}
	apiSock, apiErr := wingd.APISocketPath(home)
	if apiErr != nil {
		apiSock = ""
	}
	info, err := wingdclient.Probe(ctx, sock)
	switch {
	case err == nil:
		return DoctorDaemon{
			Reachable:     true,
			State:         ReachServing,
			Socket:        sock,
			APISocket:     apiSock,
			APIReady:      info.APIReady,
			APIError:      info.APIError,
			Version:       info.BinaryVersion,
			ProtocolMajor: info.ProtocolMajor,
		}
	case errors.Is(err, wingdclient.ErrNoDaemon):
		return DoctorDaemon{
			State:     ReachAbsent,
			Socket:    sock,
			APISocket: apiSock,
			Detail:    "nothing is listening; no admission is being arbitrated on this home",
		}
	default:
		return DoctorDaemon{State: ReachUnreachable, Socket: sock, APISocket: apiSock, Detail: err.Error()}
	}
}

func diagnoseDaemonHealth(selfVersion string, queueState wingwire.QueueState, queueRead bool, report *DoctorReport) {
	info := wingdclient.DaemonInfo{
		BinaryVersion: report.Daemon.Version,
		ProtocolMajor: report.Daemon.ProtocolMajor,
	}
	if report.Daemon.Version == "" && report.Daemon.ProtocolMajor == 0 {
		return
	}
	if versionSkewed(selfVersion, info.BinaryVersion) {
		report.DaemonVersionSkew = &DoctorVersionSkew{Self: selfVersion, Daemon: info.BinaryVersion}
	}
	diagnoseLockedOutRepos(info.ProtocolMajor, info.BinaryVersion, wingwire.ReleasedProtocolFloors(), report)

	if !queueRead {
		return
	}
	report.MachineBudget = machineBudget(queueState)
	if queueState.Events == nil {
		return
	}
	for _, r := range queueState.Events.Rejections {
		if r.Count >= doctorRejectionPatternThreshold {
			report.AdmissionRejections = append(report.AdmissionRejections,
				DoctorRejection{Cause: r.Cause, Count: r.Count})
		}
	}
}

func machineBudget(qs wingwire.QueueState) *DoctorMachineBudget {
	b := qs.Budget
	if b == nil || b.Source == "" || b.Source == string(wingwire.BudgetSourceUnset) {
		return nil
	}
	return &DoctorMachineBudget{
		Source:             b.Source,
		Origin:             b.Origin,
		Raw:                b.Raw,
		Cores:              b.Cores,
		MachineCores:       b.MachineCores,
		MemoryBytes:        b.MemoryBytes,
		MachineMemoryBytes: b.MachineMemoryBytes,
		Enforce:            b.Enforce,
		IgnoreExternal:     b.IgnoreExternal || qs.IgnoreExternal,
	}
}

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

func versionSkewed(self, daemon string) bool {
	if self == "" || daemon == "" || self == "(unknown)" || daemon == "(unknown)" {
		return false
	}
	return self != daemon
}

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

func liveDaemonRuns(queueState wingwire.QueueState, queueRead bool) map[string]struct{} {
	live := map[string]struct{}{}
	if !queueRead {
		return live
	}
	for _, h := range queueState.Holders {
		live[h.RunID] = struct{}{}
	}
	for _, w := range queueState.Waiters {
		live[w.RunID] = struct{}{}
	}
	return live
}

func probeDaemonQueue(ctx context.Context, report *DoctorReport) (wingwire.QueueState, bool) {
	if !report.Daemon.Reachable {
		return wingwire.QueueState{}, false
	}
	queueState, err := wingdclient.ProbeQueue(ctx, report.Daemon.Socket)
	if err == nil {
		return queueState, true
	}
	report.Daemon.Reachable = false
	report.Daemon.State = ReachUnreachable
	report.Daemon.Detail = "daemon handshake succeeded but its queue state could not be read: " + err.Error()
	return wingwire.QueueState{}, false
}

func diagnoseOrphanRuns(ctx context.Context, st *store.Store, daemonLive, legacyRuns map[string]struct{}, blind, dryRun bool, report *DoctorReport) error {
	if blind {
		return nil
	}
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

func diagnoseLegacyBoxSlots(boxRoot *os.Root, displayPath string, holders []boxslot.Holder, dryRun bool, report *DoctorReport) error {
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
	if boxRoot == nil {
		return nil
	}
	if dryRun {
		n, err := countRootFiles(boxRoot)
		if err != nil {
			return err
		}
		report.LegacyBoxSlotFilesRemoved = n
		return nil
	}
	removed, live, err := boxslot.PurgeIfIdleInRoot(boxRoot, displayPath)
	if err != nil {
		return err
	}
	if len(live) > 0 {
		for _, h := range live {
			report.LiveLegacyHolders = append(report.LiveLegacyHolders, DoctorLegacyHolder{
				PID: h.PID, RunID: h.RunID, Lock: h.Path,
			})
		}
		return nil
	}
	report.LegacyBoxSlotFilesRemoved = removed
	return nil
}

func countRootFiles(root *os.Root) (int, error) {
	dir, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	entries, readErr := dir.ReadDir(-1)
	if err := dir.Close(); err != nil && readErr == nil {
		readErr = err
	}
	if readErr != nil {
		return 0, readErr
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && e.Name() != "coord.lock" {
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

func diagnoseDanglingRunDirs(ctx context.Context, st *store.Store, runsRoot *os.Root, dryRun bool, report *DoctorReport) error {
	if runsRoot == nil {
		return nil
	}
	dir, err := runsRoot.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := dir.ReadDir(-1)
	if err := dir.Close(); err != nil && readErr == nil {
		readErr = err
	}
	if readErr != nil {
		return readErr
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
		if err := runsRoot.RemoveAll(e.Name()); err != nil {
			return err
		}
	}
	return nil
}

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
	fmt.Fprintf(w, "daemon\t%s\n", r.Daemon.State)
	fmt.Fprintf(w, "permission_repairs\t%d\n", len(r.PermissionRepairs))
	permissionUnverified := 0
	if r.PermissionAuditUnverified {
		permissionUnverified = 1
	}
	fmt.Fprintf(w, "permission_audit_unverified\t%d\n", permissionUnverified)
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
	fmt.Fprintf(w, "gates_surveyed\t%d\n", r.GatesSurveyed)
	surveyFailed := 0
	if r.GatesSurveyError != "" {
		surveyFailed = 1
	}
	fmt.Fprintf(w, "gates_survey_failed\t%d\n", surveyFailed)
	budgetSource, budgetOrigin, ignoreExternal := "unset", "-", 0
	if b := r.MachineBudget; b != nil {
		budgetSource = b.Source
		if b.Origin != "" {
			budgetOrigin = b.Origin
		}
		if b.IgnoreExternal {
			ignoreExternal = 1
		}
	}
	fmt.Fprintf(w, "machine_budget\t%s\t%s\t%d\n", budgetSource, budgetOrigin, ignoreExternal)
	fmt.Fprintf(w, "stray_daemons\t%d\n", len(r.StrayDaemons))
	competing := 0
	if r.InstallConflict != nil {
		competing = len(r.InstallConflict.Competing)
	}
	fmt.Fprintf(w, "competing_installs\t%d\n", competing)
	fmt.Fprintf(w, "toolchains\t%d\n", len(r.Toolchains))
	standaloneRuns := 0
	for _, sa := range r.StandaloneStores {
		if sa.Runs > 0 {
			standaloneRuns += sa.Runs
		}
	}
	fmt.Fprintf(w, "standalone_stores\t%d\t%d\n", len(r.StandaloneStores), standaloneRuns)
	return nil
}

func renderDoctorPretty(w io.Writer, r DoctorReport, legacyLine string) error {
	verb, would := "removed", ""
	if r.DryRun {
		verb, would = "found", " (dry run: nothing changed)"
	}
	if r.Clean() {
		fmt.Fprintf(w, "healthy: nothing to repair%s\n", would)
		renderDaemonSection(w, r)
		renderMachineBudget(w, r)
		renderToolchains(w, r)
		renderStandaloneStores(w, r)
		renderUngatedRepos(w, r)
		renderStrayDaemons(w, r)
		return nil
	}
	renderInstallConflict(w, r)
	renderDaemonSection(w, r)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if n := len(r.PermissionRepairs); n > 0 {
		permissionVerb := "tightened"
		if r.DryRun {
			permissionVerb = "found"
		}
		fmt.Fprintf(tw, "private paths %s\t%d\n", permissionVerb, n)
	}
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
	if len(r.PermissionRepairs) > 0 {
		fmt.Fprintln(w, "\npermissions:")
		for _, change := range r.PermissionRepairs {
			fmt.Fprintf(w, "  %q %s -> %s\n", change.Path, change.Before, change.After)
		}
	}
	if r.PermissionAuditUnverified {
		fmt.Fprintln(w, "\nwarning: local file permissions were not verified -- Windows access is governed by DACLs, which this doctor check cannot inspect or repair")
	}

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
			store.DisplayProfileKey(p.Pipeline), p.FloorCores, p.ChargeCores, p.GrantableCores, store.DisplayProfileKey(p.Pipeline))
	}

	if legacyLine != "" {
		fmt.Fprintf(w, "\nwarning: %s\n", legacyLine)
		for _, h := range r.LiveLegacyHolders {
			fmt.Fprintf(w, "  pid %d holding %s\n", h.PID, h.Lock)
		}
	}
	renderMachineBudget(w, r)
	renderToolchains(w, r)
	renderStandaloneStores(w, r)
	renderStrayDaemons(w, r)
	return nil
}

func renderDaemonSection(w io.Writer, r DoctorReport) {
	d := r.Daemon
	switch d.State {
	case ReachServing:
		version := d.Version
		if version == "" {
			version = "version unreported"
		}
		fmt.Fprintf(w, "daemon: serving, %s, protocol %d (%s)\n", version, d.ProtocolMajor, d.Socket)
		switch {
		case d.APIReady == nil:
		case *d.APIReady:
			fmt.Fprintf(w, "  controller API: serving (%s)\n", d.APISocket)
		default:
			fmt.Fprintf(w, "\nwarning: the daemon serves admission but not the controller API, so no run it hosts can reach its state\n  %s\n",
				doctorAPIFault(d))
		}
	case ReachAbsent:
		fmt.Fprintf(w, "daemon: none running -- %s\n", d.Detail)
	default:
		fmt.Fprintf(w, "\nwarning: could not reach the admission daemon, so its health is unknown\n  %s\n", d.Detail)
		if d.Socket != "" {
			fmt.Fprintf(w, "  socket: %s\n", d.Socket)
		}
		fmt.Fprint(w, "  the rejection-pattern, version-skew and lockout checks did not run, and orphaned run rows were left alone because a blind sweep cannot tell a dead run from one the daemon is holding\n")
	}
}

func doctorAPIFault(d DoctorDaemon) string {
	if d.APIError != "" {
		return d.APIError
	}
	return "the daemon did not say why"
}

func renderMachineBudget(w io.Writer, r DoctorReport) {
	b := r.MachineBudget
	if b == nil {
		return
	}
	setting := b.Origin
	if setting == "" {
		setting = "set directly by the binary that started the daemon"
	}
	fmt.Fprintf(w, "\nnote: the admission daemon runs under a machine budget (%s: %s)\n",
		b.Source, setting)
	if b.Raw != "" {
		fmt.Fprintf(w, "  setting: %s\n", b.Raw)
	}
	if b.MachineCores > 0 && b.Cores < b.MachineCores {
		fmt.Fprintf(w, "  cores capped at %.1f of the machine's %.1f\n", b.Cores, b.MachineCores)
	}
	if b.MachineMemoryBytes > 0 && b.MemoryBytes < b.MachineMemoryBytes {
		fmt.Fprintf(w, "  memory capped at %s of the machine's %s\n",
			humanBytes(b.MemoryBytes), humanBytes(b.MachineMemoryBytes))
	}
	if b.Enforce {
		fmt.Fprintf(w, "  cap hardened at the OS level\n")
	}
	if b.IgnoreExternal {
		fmt.Fprintf(w, "  external load ignored: admission plans against total capacity and subtracts no measured non-sparkwing load\n")
	}
	if b.Source == string(wingwire.BudgetSourceEnv) {
		fmt.Fprintf(w, "  this source dies with the daemon: it came from the environment of whatever process spawned it, so the next respawn may not carry it\n")
	}
}

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

func renderUngatedRepos(w io.Writer, r DoctorReport) {
	if r.GatesSurveyError != "" {
		fmt.Fprintf(w, "\nwarning: the gate survey could not run, so no repo here was checked for a gate\n  %s\n"+
			"  this is not a gated fleet, it is an unread one; fix the registry and re-run, or list it with `sparkwing configure xrepo list`\n",
			r.GatesSurveyError)
		return
	}
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
		renderGateSurveyClean(w, r)
		return
	}
	fmt.Fprintf(w, "\nwarning: %d registered repo(s) accept commits with no gate\n", len(rows))
	for _, g := range rows {
		fmt.Fprintf(w, "  %s\n    %s\n", g.Summary(), g.Remedy())
	}
	if r.GatesSurveyed > 0 {
		fmt.Fprintf(w, "  surveyed %d registered repo(s); confirm the armed ones with `sparkwing pipeline hooks fire --fleet`, which makes each gate refuse a commit\n", r.GatesSurveyed)
	}
}

func renderGateSurveyClean(w io.Writer, r DoctorReport) {
	if r.GatesSurveyed == 0 {
		return
	}
	fmt.Fprintf(w, "gates: %d registered repo(s) surveyed, every declared gate fires\n", r.GatesSurveyed)
}

func renderInstallConflict(w io.Writer, r DoctorReport) {
	c := r.InstallConflict
	if c == nil {
		return
	}
	noun := "binaries are"
	if len(c.Competing) == 1 {
		noun = "binary is"
	}
	fmt.Fprintf(w, "\nwarning: %d other sparkwing %s reachable on this machine besides the one running doctor\n",
		len(c.Competing), noun)
	fmt.Fprintf(w, "  this binary: %s\n", c.Self)
	for _, cp := range c.Competing {
		via := ""
		if cp.Resolved != "" {
			via = " -> " + cp.Resolved
		}
		fmt.Fprintf(w, "  also installed: %s%s (modified %s)\n", cp.Path, via, cp.Modified)
		fmt.Fprintf(w, "    retire it: %s\n", installsite.RetireRemedy(cp.Path).Text())
	}
	fmt.Fprintf(w, "  which one a caller gets depends on whose PATH resolved it: an interactive shell and a launchd, systemd, or cron job order theirs differently, so the same command can be two different builds\n")
	fmt.Fprintf(w, "  each install keeps its own version memory under the sparkwing home, so they cannot rewrite each other's records -- but their outputs are evidence from different builds\n")
	fmt.Fprintf(w, "  to resolve: keep one and retire the rest with the guidance above, or point each job at the absolute path of the copy you mean\n")
}

func renderToolchains(w io.Writer, r DoctorReport) {
	if len(r.Toolchains) == 0 {
		return
	}
	fmt.Fprintf(w, "\ntoolchain store: %d CLI release(s) fetched for repos whose SDK pin outranks the installed CLI\n", len(r.Toolchains))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, t := range r.Toolchains {
		fmt.Fprintf(tw, "  %s\t%s\t%d bytes\n", t.Version, t.Path, t.Bytes)
	}
	_ = tw.Flush()
}

func renderStandaloneStores(w io.Writer, r DoctorReport) {
	if len(r.StandaloneStores) == 0 {
		return
	}
	fmt.Fprintf(w, "\nstandalone runs stores: %d, written by runs that could not reach the daemon\n"+
		"  `sparkwing runs` and the dashboard read this home's own store and never these, which is what those runs said on stderr\n"+
		"  nothing prunes them; delete a directory once you no longer want its runs\n", len(r.StandaloneStores))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, sa := range r.StandaloneStores {
		if sa.Runs < 0 {
			fmt.Fprintf(tw, "  %s\tunreadable\t-\n", sa.Path)
			continue
		}
		oldest := "-"
		if sa.OldestRunAt != nil {
			oldest = "oldest " + standaloneAge(*sa.OldestRunAt)
		}
		fmt.Fprintf(tw, "  %s\t%d run(s)\t%s\n", sa.Path, sa.Runs, oldest)
	}
	_ = tw.Flush()
}

func standaloneAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func renderStrayDaemons(w io.Writer, r DoctorReport) {
	for _, d := range r.StrayDaemons {
		fmt.Fprintf(w, "\nwarning: an admission daemon for another sparkwing home reports version %s, which no release carries\n"+
			"  %s is what a binary built from a module that replaces the sparkwing SDK with a local checkout reports, so this daemon came from a scratch module -- typically one a test scaffolded under a temp directory and left running, whose process arguments still name that temp path\n"+
			"  it is not this machine's resident daemon: its log, its bind failures, and its queue describe nothing in production. Stop that process before diagnosing anything from it\n"+
			"  socket: %s\n", d.Version, d.Version, d.Socket)
	}
}
