package orchestrator

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	standaloneNoDaemon    = "no-daemon"
	standaloneDaemonOlder = "daemon-older"
	standaloneDaemonFault = "daemon-fault"
	standaloneFloor       = "floor"
	standaloneForced      = "forced"
)

// StandaloneStateDBEnv names the runs store a parent already chose, so a child
// run it dispatches lands beside it instead of deriving a store of its own.
const StandaloneStateDBEnv = "SPARKWING_STATE_DB"

// StandaloneReasonEnv carries the parent's standalone reason to that child, so
// the child's start record says what the parent's says.
const StandaloneReasonEnv = "SPARKWING_STANDALONE_REASON"

// safety: one run's standalone decision, carried from the store choice to the
// point admission answers. The block is printed only once admission has, so a
// run that is refused prints nothing reassuring and leaves no store it made.
type standaloneRun struct {
	reason    string
	stateDB   string
	warning   string
	skew      bool
	created   bool
	refused   bool
	announced bool
	lock      *standaloneLock
}

func (sa *standaloneRun) state(hosted bool) unhosted {
	if sa == nil {
		return unhosted{hosted: hosted}
	}
	return unhosted{reason: sa.reason, skew: sa.skew, hosted: hosted}
}

func (sa *standaloneRun) announce() {
	if sa == nil || sa.announced {
		return
	}
	sa.announced = true
	if sa.warning != "" {
		fmt.Fprint(standaloneWarningOut, sa.warning)
	}
}

// safety: a run refused by admission never started, so a store it created for
// itself is discarded and no block promises that everything else worked. Every
// other ending, including a setup failure, wrote a run row and says so.
func (sa *standaloneRun) settle() {
	if sa == nil || sa.announced {
		return
	}
	if sa.refused {
		sa.discard()
		return
	}
	sa.announce()
}

// safety: only the run that created the file may remove it, and only while it
// can take the lock every standalone opener holds for the life of its handle.
// Without that check a second run's discard unlinks a store the first is still
// writing to, and the rows go to a deleted inode.
func (sa *standaloneRun) discard() {
	if !sa.created || sa.stateDB == "" || !sa.lock.exclusive() {
		return
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(sa.stateDB + suffix)
	}
}

type standaloneLock struct{ f *os.File }

// safety: the lock file is never removed. Unlinking it would let the next
// opener create a second inode and hold a lock that guards nothing, which is
// the mutual exclusion this exists for.
func acquireStandaloneLock(dir string) (*standaloneLock, error) {
	f, err := fssecure.OpenFile(filepath.Join(dir, "state.lock"), os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, err
	}
	if err := flockShared(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &standaloneLock{f: f}, nil
}

func (l *standaloneLock) release() {
	if l == nil {
		return
	}
	_ = flockUnlock(l.f)
	_ = l.f.Close()
}

// safety: dropping this run's own share first is what lets the attempt see
// every other holder; failing to take it back means someone else has the store
// open, and the caller leaves their file alone.
func (l *standaloneLock) exclusive() bool {
	if l == nil {
		return false
	}
	if err := flockUnlock(l.f); err != nil {
		return false
	}
	ok, err := flockTry(l.f)
	return err == nil && ok
}

// hack: an indirection so a test can read the block a run prints.
var standaloneWarningOut io.Writer = os.Stderr

const standaloneLoss = "It cannot see other runs on this machine and they cannot see it, " +
	"so together they may oversubscribe it. Everything else works."

// safety: only a gap in what the daemon can serve degrades. A machine fault
// keeps its own error, because a run that looks like it worked is the worst
// place to hide one.
func standaloneReasonFor(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, wingdclient.ErrProtocolTooOld):
		return standaloneFloor
	case errors.Is(err, wingdclient.ErrDaemonTooOld),
		errors.Is(err, wingdclient.ErrDaemonLacksOperation),
		errors.Is(err, client.ErrControllerLacksRoute),
		errors.Is(err, errHostedStoreSkew):
		return standaloneDaemonOlder
	case errors.Is(err, wingdclient.ErrNoDaemonHost),
		errors.Is(err, wingdclient.ErrDaemonHostUnusable),
		errors.Is(err, wingdclient.ErrDaemonHostFailed):
		return standaloneNoDaemon
	}
	return ""
}

func standaloneWarning(sel hostedSelection, sdkVersion string) string {
	daemon := bareVersion(sel.daemon)
	sdk := bareVersion(sdkVersion)
	switch sel.standalone {
	case standaloneNoDaemon:
		return "sparkwing: no admission daemon is running and no sparkwing is installed to host one, " +
			"so this run is standalone. " + standaloneLoss +
			"\n\n  to host one\n    " + installAdvice + "\n"
	case standaloneDaemonOlder:
		return fmt.Sprintf("sparkwing: the admission daemon (%s) predates this pipeline's SDK (%s), "+
			"so this run is standalone. "+standaloneLoss+"%s"+
			"\n\n  to update the daemon\n    sparkwing update\n", daemon, sdk, alsoReported(sel.fault))
	case standaloneFloor:
		return fmt.Sprintf("sparkwing: this pipeline's SDK (%s) predates what the admission daemon can serve, "+
			"so this run is standalone. "+standaloneLoss+
			"\n\n  to update every repo on this machine\n    sparkwing repos update --apply"+
			"\n\n  to update just this repo\n    sparkwing repos update --apply --repo .\n", sdk)
	case standaloneDaemonFault:
		return fmt.Sprintf("sparkwing: the admission daemon (%s) cannot serve this run's state (%s), "+
			"so this run is standalone. "+standaloneLoss+
			"\n\n  to see why\n    sparkwing daemon status\n", daemon, sel.fault)
	case standaloneForced:
		return "sparkwing: " + AllowUnadmittedEnv + " is set, so this run is standalone. " + standaloneLoss +
			"\n\n  to rejoin the daemon\n    unset " + AllowUnadmittedEnv + "\n"
	}
	return ""
}

// safety: a daemon can be behind and also have failed to bind, and the remedy
// for the second is not the one the block names, so its own reason is carried
// rather than dropped for being on the older branch.
func alsoReported(fault string) string {
	if fault == "" {
		return ""
	}
	return "\nThe daemon also reported: " + fault + "."
}

// safety: the version is wrapped in parentheses by every block, so one that
// already carries them, as a source build's "(devel)" does, must not double
// them.
func bareVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "unknown"
	}
	if trimmed, ok := strings.CutPrefix(v, "("); ok {
		if inner, closed := strings.CutSuffix(trimmed, ")"); closed {
			return inner
		}
	}
	return v
}

// safety: the shared standalone store is the one the design promises two pins
// can share, so a binary falls back to its own only when that file records a
// requirement it does not know; every other open error is the store's own and
// is reported rather than routed around.
func openStandaloneStore(paths Paths, dryRun bool) (*store.Store, *standaloneRun, func(), error) {
	if dryRun {
		return openThrowawayStandaloneStore()
	}
	if err := paths.EnsureStandaloneDir(); err != nil {
		return nil, nil, nil, fmt.Errorf("standalone store: %w", err)
	}
	lock, err := acquireStandaloneLock(paths.StandaloneDir())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("standalone store: %w", err)
	}
	release := func() { lock.release() }

	shared := paths.StandaloneStateDB()
	fresh := claimStoreFile(shared)
	st, err := storeOpen(shared)
	if err == nil {
		return st, &standaloneRun{stateDB: shared, created: fresh, lock: lock}, release, nil
	}
	var skew *store.SkewError
	if !errors.As(err, &skew) || len(skew.Requirements) == 0 {
		release()
		return nil, nil, nil, fmt.Errorf("standalone store %s: %w", shared, err)
	}
	if err := paths.EnsureStandaloneSchemaDir(); err != nil {
		release()
		return nil, nil, nil, fmt.Errorf("standalone store: %w", err)
	}
	own := paths.StandaloneSchemaStateDB()
	fresh = claimStoreFile(own)
	st, err = storeOpen(own)
	if err != nil {
		release()
		return nil, nil, nil, fmt.Errorf("standalone store %s: %w", own, err)
	}
	return st, &standaloneRun{stateDB: own, created: fresh, lock: lock}, release, nil
}

// safety: creation is observed rather than guessed. Two runs starting together
// on a fresh box both stat an absent file, and both would then believe they may
// remove it; an exclusive create hands that claim to exactly one of them.
func claimStoreFile(path string) bool {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	return f.Close() == nil
}

// safety: a dry run mutates nothing, so it must not be what creates this
// home's standalone store or leave rows doctor then counts against it.
func openThrowawayStandaloneStore() (*store.Store, *standaloneRun, func(), error) {
	dir, err := os.MkdirTemp("", "sparkwing-dry-standalone-")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("standalone store: %w", err)
	}
	path := filepath.Join(dir, "state.db")
	st, err := storeOpen(path)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, nil, nil, fmt.Errorf("standalone store %s: %w", path, err)
	}
	return st, &standaloneRun{stateDB: path}, func() { _ = os.RemoveAll(dir) }, nil
}
