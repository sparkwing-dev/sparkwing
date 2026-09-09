package crons

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// PinnedBinaryName is the file a locked schedule runs, under
// <Service.PinRoot>/<schedule id>/.
const PinnedBinaryName = "pipeline"

// Proof is what compiling a repository's pipelines produced: the binary that
// answers for the pipeline, and the cache digest it was built from.
type Proof struct {
	Binary string
	Digest string
}

// Prover compiles a repository's pipelines and reports the binary that carries
// the named one. Arming refuses the whole repository when it fails, because a
// schedule fires unattended and a pipeline that will not build is refused here
// rather than at three in the morning. The binary it names is what a pinned
// schedule runs.
type Prover func(ctx context.Context, repoRoot, pipeline string) (Proof, error)

// ArmOptions shapes one arm.
type ArmOptions struct {
	// Only restricts the arm to these pipelines or "pipeline/name" entries.
	// Empty arms every entry the repository declares for this side. A name
	// the repository does not declare is an error before anything is written.
	Only []string
	// Follow arms without pinning, so every fire compiles the checkout.
	Follow bool
	// Prove compiles the pipelines and names the binary to pin. Nil arms
	// without compiling, which also leaves nothing to pin.
	Prove Prover
}

// ArmReport says what arming one repository changed. Schedules holds every row
// the repository still declares for this host, created or refreshed;
// Withdrawals names the display names of the rows it stopped declaring, and
// Controller the entries that fire from a controller and were left alone.
type ArmReport struct {
	Armed       int                  `json:"armed"`
	Refreshed   int                  `json:"refreshed"`
	Withdrawn   int                  `json:"withdrawn"`
	Schedules   []store.CronSchedule `json:"schedules,omitempty"`
	Withdrawals []string             `json:"withdrawals,omitempty"`
	Controller  []string             `json:"controller,omitempty"`
}

// Arm records the schedules repoRoot declares with `where: local` against this
// home and computes each one's next due instant. A row that already exists
// keeps its pause state, its cursor, its history and this host's override;
// only the declaration and the lock are republished. A row this repository no
// longer declares is marked undeclared rather than deleted, so its history
// stays readable. Entries declared `where: controller` are reported and never
// stored here.
//
// Unless [ArmOptions.Follow], the arm pins: the compiled binary is copied
// under [Service.PinRoot] and recorded on the row with the checkout's HEAD, so
// an updated checkout cannot change what an unattended run executes.
// [ArmOptions.Prove] runs once per selected pipeline before anything is
// written, and its first failure aborts the whole repository with that reason,
// so a repository is never half armed.
func (s *Service) Arm(ctx context.Context, repoRoot string, opts ArmOptions) (ArmReport, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return ArmReport{}, fmt.Errorf("resolve %s: %w", repoRoot, err)
	}
	declared, err := DeclaredSchedules(root)
	if err != nil {
		return ArmReport{}, err
	}
	selected, err := selectDeclared(declared, opts.Only, root)
	if err != nil {
		return ArmReport{}, err
	}

	var report ArmReport
	var locals []Declared
	for _, d := range selected {
		if d.Local() {
			locals = append(locals, d)
			continue
		}
		report.Controller = append(report.Controller, d.DisplayName())
	}

	proofs, err := s.proveAll(ctx, root, locals, opts.Prove)
	if err != nil {
		return ArmReport{}, err
	}
	head := ""
	if !opts.Follow && len(proofs) > 0 {
		head = readCheckout(ctx, root).head
	}

	now := s.now()
	for _, d := range locals {
		lock := store.CronLock{}
		if proof, pinned := proofs[d.Pipeline]; pinned && !opts.Follow {
			lock, err = s.pin(d.ID(), head, proof)
			if err != nil {
				return report, fmt.Errorf("%s: %w", d.DisplayName(), err)
			}
		}
		row, rerr := scheduleRow(d, lock, s.ArmedBy, now)
		if rerr != nil {
			return report, fmt.Errorf("%s: %w", d.DisplayName(), rerr)
		}
		stored, created, aerr := s.Store.ArmCronSchedule(ctx, row, now)
		if aerr != nil {
			return report, aerr
		}
		if created {
			report.Armed++
		} else {
			report.Refreshed++
		}
		if rebased, berr := s.rebaseOverride(ctx, stored, now); berr != nil {
			return report, berr
		} else if rebased != nil {
			stored = *rebased
		}
		report.Schedules = append(report.Schedules, stored)
	}

	if err := s.withdrawUndeclared(ctx, root, declared, now, &report); err != nil {
		return report, err
	}
	sort.Strings(report.Withdrawals)
	return report, nil
}

// safety: a name the repository does not declare fails the whole arm before
// anything is written, so a typo never half-arms a checkout.
func selectDeclared(declared []Declared, only []string, root string) ([]Declared, error) {
	if len(only) == 0 {
		return declared, nil
	}
	var out []Declared
	for _, want := range only {
		want = strings.TrimSpace(want)
		if want == "" {
			continue
		}
		var matched []Declared
		for _, d := range declared {
			if d.Pipeline == want || d.Selector() == want {
				matched = append(matched, d)
			}
		}
		if len(matched) == 0 {
			return nil, fmt.Errorf("--only %s: %s declares no schedule by that name; it declares %s",
				want, root, declaredNames(declared))
		}
		out = append(out, matched...)
	}
	return out, nil
}

func declaredNames(declared []Declared) string {
	if len(declared) == 0 {
		return "none"
	}
	names := make([]string, 0, len(declared))
	for _, d := range declared {
		names = append(names, d.Selector())
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func (s *Service) proveAll(ctx context.Context, root string, locals []Declared, prove Prover) (map[string]Proof, error) {
	if prove == nil {
		return nil, nil
	}
	proofs := map[string]Proof{}
	for _, d := range locals {
		if _, done := proofs[d.Pipeline]; done {
			continue
		}
		proof, err := prove(ctx, root, d.Pipeline)
		if err != nil {
			return nil, fmt.Errorf("%s does not compile, so nothing in %s was armed: %w",
				d.Pipeline, root, err)
		}
		proofs[d.Pipeline] = proof
	}
	return proofs, nil
}

// safety: the arm re-reads the declaration it just wrote, so an override that
// survived the arm is measured against what the repository says now rather
// than what it said when the override was typed.
func (s *Service) rebaseOverride(ctx context.Context, stored store.CronSchedule, now time.Time) (*store.CronSchedule, error) {
	if stored.Override == nil || !overrideStale(stored) {
		return nil, nil
	}
	override := *stored.Override
	override.Base = stored.Declaration()
	if err := s.Store.SetCronOverride(ctx, stored.ID, override, now); err != nil {
		return nil, fmt.Errorf("%s: rebase the override onto the new declaration: %w", DisplayName(stored), err)
	}
	stored.Override = &override
	return &stored, nil
}

func (s *Service) withdrawUndeclared(
	ctx context.Context, root string, declared []Declared, now time.Time, report *ArmReport,
) error {
	keep := make(map[string]bool, len(declared))
	for _, d := range declared {
		if d.Local() {
			keep[d.ID()] = true
		}
	}
	existing, err := s.Store.ListCronSchedules(ctx)
	if err != nil {
		return err
	}
	for _, sched := range existing {
		if sched.RepoPath != root || keep[sched.ID] || !sched.Declared {
			continue
		}
		if err := s.Store.SetCronScheduleDeclared(ctx, sched.ID, false, now); err != nil {
			return err
		}
		report.Withdrawn++
		report.Withdrawals = append(report.Withdrawals, DisplayName(sched))
	}
	return nil
}

// safety: a re-arm replaces the pinned file in place, so a tick firing during
// an arm execs either the old binary or the new one and never a partial write.
func (s *Service) pin(id, head string, proof Proof) (store.CronLock, error) {
	if proof.Binary == "" {
		return store.CronLock{}, nil
	}
	if s.PinRoot == "" {
		return store.CronLock{}, errors.New("no directory is configured to hold pinned pipeline binaries")
	}
	dir := filepath.Join(s.PinRoot, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return store.CronLock{}, fmt.Errorf("make %s: %w", dir, err)
	}
	dest := filepath.Join(dir, PinnedBinaryName)
	if err := copyExecutable(proof.Binary, dest); err != nil {
		return store.CronLock{}, err
	}
	digest := proof.Digest
	if digest == "" {
		computed, err := fileDigest(dest)
		if err != nil {
			return store.CronLock{}, fmt.Errorf("digest %s: %w", dest, err)
		}
		digest = computed
	}
	return store.CronLock{Ref: head, Binary: dest, Digest: digest}, nil
}

func (s *Service) removePin(id string) error {
	if s.PinRoot == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(s.PinRoot, id))
}

// safety: written beside the destination and renamed, so a tick that fires
// mid-arm execs either the old binary or the new one and never a partial file.
func copyExecutable(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("read the compiled pipeline at %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.CreateTemp(filepath.Dir(dest), PinnedBinaryName+"-*")
	if err != nil {
		return fmt.Errorf("stage %s: %w", dest, err)
	}
	staged := tmp.Name()
	defer discardStaged(staged)
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("copy the compiled pipeline to %s: %w", staged, err)
	}
	if err := tmp.Chmod(0o700); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("make %s executable: %w", staged, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", staged, err)
	}
	if err := os.Rename(staged, dest); err != nil {
		return fmt.Errorf("publish %s: %w", dest, err)
	}
	return nil
}

// safety: a staged copy left behind is harmless next to the arm's own failure,
// so it is logged rather than returned.
func discardStaged(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Default().Warn("staged pipeline binary outlived its arm", "path", path, "error", err)
	}
}

// safety: the arm records a digest even when the compiler exposes no cache
// key, so two pins of the same schedule are still comparable.
func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// safety: the next due instant is computed here so a freshly armed schedule reports one before the first tick.
func scheduleRow(d Declared, lock store.CronLock, armedBy string, now time.Time) (store.CronSchedule, error) {
	parsed, err := cronspec.Parse(d.Trigger.Cron)
	if err != nil {
		return store.CronSchedule{}, err
	}
	loc, err := d.Trigger.Location()
	if err != nil {
		return store.CronSchedule{}, err
	}
	catchUp, err := d.Trigger.CatchUpDuration()
	if err != nil {
		return store.CronSchedule{}, err
	}
	row := store.CronSchedule{
		ID:           d.ID(),
		RepoPath:     d.RepoPath,
		Pipeline:     d.Pipeline,
		Name:         d.Name,
		Cron:         d.Trigger.Cron,
		TZ:           d.Trigger.TZ,
		Overlap:      d.Trigger.OverlapPolicy(),
		CatchUp:      catchUp,
		Where:        d.Trigger.Where,
		Args:         d.Trigger.Args,
		LockedRef:    lock.Ref,
		LockedBinary: lock.Binary,
		LockedDigest: lock.Digest,
		ArmedAt:      now,
		ArmedBy:      armedBy,
	}
	if next := parsed.Next(now, loc); !next.IsZero() {
		row.NextDueAt = &next
	}
	return row, nil
}

// Disarm deletes one schedule, its history and its pinned binary.
func (s *Service) Disarm(ctx context.Context, id string) error {
	if _, err := s.Store.GetCronSchedule(ctx, id); err != nil {
		return err
	}
	if err := s.Store.DeleteCronSchedule(ctx, id); err != nil {
		return err
	}
	return s.removePin(id)
}

// DisarmRepo deletes every schedule of one repository checkout, their
// histories and their pinned binaries, returning how many rows went.
func (s *Service) DisarmRepo(ctx context.Context, repoRoot string) (int, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return 0, fmt.Errorf("resolve %s: %w", repoRoot, err)
	}
	rows, err := s.Store.ListCronSchedules(ctx)
	if err != nil {
		return 0, err
	}
	removed, err := s.Store.DeleteCronSchedulesForRepo(ctx, root)
	if err != nil {
		return 0, err
	}
	for _, sched := range rows {
		if sched.RepoPath != root {
			continue
		}
		if perr := s.removePin(sched.ID); perr != nil {
			return removed, perr
		}
	}
	return removed, nil
}

// Lock pins a schedule to the checkout as it stands: it compiles the pipeline
// through prove, copies that binary under [Service.PinRoot], and records the
// checkout's HEAD. The schedule then runs that binary until it is re-pinned or
// unlocked, whatever the checkout does afterward.
func (s *Service) Lock(ctx context.Context, id string, prove Prover) (store.CronSchedule, error) {
	sched, err := s.Store.GetCronSchedule(ctx, id)
	if err != nil {
		return store.CronSchedule{}, err
	}
	if prove == nil {
		return store.CronSchedule{}, fmt.Errorf("%s: pinning needs to compile the pipeline, so it cannot be done without proof",
			DisplayName(sched))
	}
	proof, err := prove(ctx, sched.RepoPath, sched.Pipeline)
	if err != nil {
		return store.CronSchedule{}, fmt.Errorf("%s: %w", DisplayName(sched), err)
	}
	lock, err := s.pin(sched.ID, readCheckout(ctx, sched.RepoPath).head, proof)
	if err != nil {
		return store.CronSchedule{}, fmt.Errorf("%s: %w", DisplayName(sched), err)
	}
	if err := s.Store.SetCronScheduleLock(ctx, sched.ID, lock, s.now()); err != nil {
		return store.CronSchedule{}, err
	}
	return s.Store.GetCronSchedule(ctx, sched.ID)
}

// Unlock drops a schedule's pin and its pinned binary, so every later fire
// compiles the checkout again.
func (s *Service) Unlock(ctx context.Context, id string) (store.CronSchedule, error) {
	sched, err := s.Store.GetCronSchedule(ctx, id)
	if err != nil {
		return store.CronSchedule{}, err
	}
	if err := s.Store.SetCronScheduleLock(ctx, sched.ID, store.CronLock{}, s.now()); err != nil {
		return store.CronSchedule{}, err
	}
	if err := s.removePin(sched.ID); err != nil {
		return store.CronSchedule{}, err
	}
	return s.Store.GetCronSchedule(ctx, sched.ID)
}

// RefreshReport says what one pass over the armed repositories changed.
// Updated counts the rows whose declaration actually moved, not the rows
// looked at. Errors carries one sentence per repository that could not be
// read; a repository that fails never blocks the others.
type RefreshReport struct {
	Repos     int      `json:"repos"`
	Updated   int      `json:"updated"`
	Withdrawn int      `json:"withdrawn"`
	Locked    int      `json:"locked"`
	Errors    []string `json:"errors,omitempty"`
}

// safety: the refresh runs every minute over every armed row, so an unchanged
// declaration must not write: an updated_at that moves each minute tells an
// operator nothing and costs a transaction per schedule.
func republished(stored, declared store.CronSchedule) bool {
	return stored.Declared &&
		stored.Cron == declared.Cron &&
		stored.TZ == declared.TZ &&
		stored.Overlap == declared.Overlap &&
		stored.CatchUp == declared.CatchUp &&
		stored.Where == declared.Where &&
		sameArgs(stored.Args, declared.Args)
}

func sameArgs(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// Refresh re-reads every armed repository's sparkwing.yaml and republishes
// what it finds, without proof: a changed cadence, zone, overlap policy,
// catch-up window or argument set is stored, and a row the repository stopped
// declaring is marked undeclared. A checkout that cannot be read at all is
// reported in Errors and its rows are left alone, because a detached volume or
// a moved directory is not a decision to stop scheduling.
//
// A locked schedule is left alone entirely: its declaration is what was armed
// with it, and reading the working tree is exactly what the pin exists to
// avoid. What the checkout has done since is derived on read instead, and
// reaches an operator through the lock state in `sparkwing crons list`.
//
// Refresh republishes rows that already exist. A pipeline that starts
// declaring a schedule is armed by `sparkwing crons install`, because which
// host evaluates a schedule is a decision an operator makes on that host.
func (s *Service) Refresh(ctx context.Context) (RefreshReport, error) {
	stored, err := s.Store.ListCronSchedules(ctx)
	if err != nil {
		return RefreshReport{}, err
	}
	byRepo := map[string][]store.CronSchedule{}
	var roots []string
	for _, sched := range stored {
		if _, seen := byRepo[sched.RepoPath]; !seen {
			roots = append(roots, sched.RepoPath)
		}
		byRepo[sched.RepoPath] = append(byRepo[sched.RepoPath], sched)
	}
	sort.Strings(roots)

	now := s.now()
	var report RefreshReport
	for _, root := range roots {
		if PushedRepo(root) {
			// safety: a pushed repository's declaration is what the push
			// carried, and this process holds no checkout of it to read.
			report.Locked += len(byRepo[root])
			continue
		}
		report.Repos++
		declared, derr := DeclaredSchedules(root)
		if derr != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", root, derr))
			continue
		}
		byID := make(map[string]Declared, len(declared))
		for _, d := range declared {
			if d.Local() {
				byID[d.ID()] = d
			}
		}
		for _, sched := range byRepo[root] {
			if sched.LockedBinary != "" || Pushed(sched) {
				report.Locked++
				continue
			}
			s.refreshOne(ctx, sched, byID, now, &report)
		}
	}
	return report, nil
}

func (s *Service) refreshOne(
	ctx context.Context, sched store.CronSchedule, byID map[string]Declared,
	now time.Time, report *RefreshReport,
) {
	d, still := byID[sched.ID]
	if !still {
		if !sched.Declared {
			return
		}
		if err := s.Store.SetCronScheduleDeclared(ctx, sched.ID, false, now); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", DisplayName(sched), err))
			return
		}
		report.Withdrawn++
		return
	}
	row, rerr := scheduleRow(d, store.CronLock{}, sched.ArmedBy, now)
	if rerr != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", DisplayName(sched), rerr))
		return
	}
	if republished(sched, row) {
		return
	}
	if _, _, err := s.Store.ArmCronSchedule(ctx, row, now); err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", DisplayName(sched), err))
		return
	}
	report.Updated++
}
