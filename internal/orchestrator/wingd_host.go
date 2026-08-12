// Admission wiring for the compiled pipeline binary.
//
// The invariant: the installed Sparkwing distribution owns daemon
// lifecycle. Pipeline clients declare required capabilities and use the
// running daemon; they never host, replace, or upgrade it. A repo's
// .sparkwing/go.mod pin is therefore not part of the machine-wide daemon
// version negotiation, and one repo bumping its SDK cannot churn the
// daemon every other repo on the box shares.
//
// What a pipeline binary does when no daemon is reachable and none can be
// hosted depends on what its pipeline asked for -- see
// [planDeclaresLocalAdmission] and [LocalAdmission.unhostedOutcome].
package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// AllowUnadmittedEnv forces a run that explicitly declares resource
// claims or concurrency groups to proceed uncoordinated when no daemon
// is reachable and none can be hosted, instead of failing closed.
//
// It is an environment variable rather than a `--sw-allow` category
// because the runs that need it are exactly the ones no CLI launched: a
// pipeline binary shipped alone to a deploy box or run from a systemd
// unit never parses `--sw-*` flags. `--sw-allow` also carries a
// different meaning -- author-declared blast-radius labels the operator
// acknowledges -- and a synthetic label in that set would be checked
// against a pipeline's own declarations, which this is not.
const AllowUnadmittedEnv = "SPARKWING_ALLOW_UNADMITTED"

// installAdvice is the fix named by both fail-closed errors: the lever is
// the machine's sparkwing installation, never the repo's pin.
const installAdvice = "install or update the sparkwing CLI on this host, " +
	"or set " + wingdclient.HostBinEnv + " to an installed sparkwing binary"

// pipelineAdmission builds the LocalAdmission for the compiled pipeline
// binary's entry points (the local run path and handle-trigger --local).
// Its client spawns the installed sparkwing as the daemon host --
// resolved from $SPARKWING_WINGD_BIN, which `sparkwing run` sets before
// exec'ing this binary, else a `sparkwing` on PATH -- and never drains,
// replaces, or upgrades a running daemon.
//
// It always returns an admission. Whether a run may proceed without one
// cannot be known here: it depends on the plan, which does not exist
// until the pipeline's entrypoint has been invoked. The decision is made
// once, at run start, by [LocalAdmission.unhostedOutcome].
func pipelineAdmission(parentLeaseToken string, origin wingwire.Origin) *LocalAdmission {
	spawn, ok := wingdclient.HostSpawn()
	if !ok {
		spawn = wingdclient.NoHostSpawn
	}
	return &LocalAdmission{
		Version:          sparkwingModuleVersion(),
		ParentLeaseToken: parentLeaseToken,
		Origin:           origin,
		Spawn:            spawn,
		PipelineClient:   true,
	}
}

// planPinsHostResources reports whether a run explicitly reserved host
// capacity, and names where, for the error that fails such a run closed.
// True when the plan carries a plan-level .Resources() pin
// ([sparkwing.Plan.ResourceHints] with a non-zero core or memory figure)
// or any node carries one.
//
// A host-resource pin is the only declaration with no fallback. CPU and
// memory are arbitrated by the daemon and by nothing else, so a pinned
// run on a box with no daemon does not get a weaker version of what it
// asked for -- it gets none of it, silently, while other work on the box
// does the same.
//
// .Concurrency() groups are deliberately excluded, even box- and
// run-scoped ones the daemon would otherwise arbitrate. Without an
// admission the whole run takes the no-daemon path, where those groups
// are honored by the shared store instead: acquirePlanSlot stops
// filtering local scopes out when the run has no admission, and
// runUnderGroup routes a node's group to the store acquire whenever the
// context carries no LocalAdmission. Run scope is keyed by run id, so the
// store enforces it completely. Refusing those runs would defend nothing
// and would break the ordinary "one deploy at a time on this box" pipeline
// on exactly the bare hosts this feature is supposed to keep working.
//
// What the store path gives up is crash cleanup, not exclusion: the
// daemon releases a dead holder's slot the moment the kernel closes its
// socket, while a store slot outlives a killed run until `sparkwing
// doctor` reclaims it. That is a real difference, and the degrade warning
// says so -- but it is a reason to warn, not a reason to refuse.
func planPinsHostResources(plan *sparkwing.Plan) (pinned bool, where string) {
	if plan == nil {
		return false, ""
	}
	planLevel := false
	if h := plan.ResourceHints(); h != nil && (h.Cores > 0 || h.MemoryBytes > 0) {
		planLevel = true
	}
	var nodes []string
	for _, n := range plan.Nodes() {
		if h := n.ResourceHints(); h != nil && (h.Cores > 0 || h.MemoryBytes > 0) {
			nodes = append(nodes, strconv.Quote(n.ID()))
		}
	}
	switch {
	case planLevel && len(nodes) > 0:
		sort.Strings(nodes)
		return true, "a plan-level .Resources() pin, and node-level pins on " + strings.Join(nodes, ", ")
	case planLevel:
		return true, "a plan-level .Resources() pin"
	case len(nodes) > 0:
		sort.Strings(nodes)
		noun := "a node-level .Resources() pin on "
		if len(nodes) > 1 {
			noun = "node-level .Resources() pins on "
		}
		return true, noun + strings.Join(nodes, ", ")
	}
	return false, ""
}

// allowUnadmitted reports whether the operator has authorized a run to
// proceed uncoordinated. Only the exact value "1" counts.
//
// Strict on purpose: this switch turns off a safety check, and every way
// of writing it down that is not plainly "on" should leave the check on.
// A lenient parser that reads "off" as unset -- non-empty, not one of the
// spellings it happens to know -- disables the gate for someone trying to
// keep it.
func allowUnadmitted() bool {
	return os.Getenv(AllowUnadmittedEnv) == "1"
}

// unhostedOutcome classifies a run-start admission failure for a client
// that cannot host the daemon.
//
// It answers only the two conditions that mean "this box offers no
// admission this client can use, and no action of this client's can
// change that", and it answers them differently, because they are not the
// same situation:
//
//   - [wingdclient.ErrNoDaemonHost] -- nothing is listening and no
//     installed sparkwing exists to start one. Nothing is arbitrating this
//     box, so an unadmitted run is not competing with an admitted one.
//     A run that pinned host resources still fails closed: CPU and memory
//     have no fallback arbiter, and quietly not reserving what a pipeline
//     reserved is a different pipeline. Everything else degrades with one
//     warning, which is what keeps a pipeline binary shipped alone to a
//     deploy box a working product.
//
//   - [wingdclient.ErrDaemonTooOld] -- a daemon answered, is arbitrating
//     this box right now, and speaks a protocol this client cannot use.
//     Every run refuses, pinned or not. Proceeding would put unadmitted
//     work alongside runs the daemon is holding capacity for, which
//     oversubscribes the box in exactly the way admission exists to
//     prevent -- and unlike the case above, there is a live arbiter whose
//     accounting we would be corrupting. A live arbiter you cannot join is
//     not the same as no arbiter.
//
// [AllowUnadmittedEnv] forces the degrade in both cases: an operator who
// knows what else runs on the box can overrule either judgment.
//
// A dry run is exempt. It executes DryRunFn bodies, mutates nothing, and
// finishes in seconds, so it neither needs the capacity it declared nor
// meaningfully competes for it -- and refusing it would block the one
// command an operator reaches for to find out what a box would do.
//
// Every other failure -- unreachable, exhausted takeover, an unusable host
// binary, a policy rejection, a queue timeout -- returns (false, nil) and
// the caller keeps the original error.
func (la *LocalAdmission) unhostedOutcome(err error, plan *sparkwing.Plan, dryRun bool) (degrade bool, failure error) {
	if la == nil || !la.PipelineClient || err == nil {
		return false, nil
	}
	noHost := errors.Is(err, wingdclient.ErrNoDaemonHost)
	tooOld := errors.Is(err, wingdclient.ErrDaemonTooOld)
	if !noHost && !tooOld {
		return false, nil
	}
	if !dryRun && !allowUnadmitted() {
		if tooOld {
			return false, staleDaemonRefusal(err)
		}
		if pinned, where := planPinsHostResources(plan); pinned {
			return false, unpinnedHostRefusal(where)
		}
	}
	la.unadmittedOnce.Do(func() {
		if tooOld {
			la.logf("%v; running without local coordination", err)
			return
		}
		la.logf("no admission daemon is running and no sparkwing is installed to host one; "+
			"running without local coordination -- host CPU and memory are not arbitrated, so concurrent runs on this box "+
			"can oversubscribe it. Box- and run-scoped .Concurrency() groups are still enforced, through the shared store "+
			"rather than the daemon, which releases a killed run's slot only when `sparkwing doctor` reclaims it. "+
			"To coordinate the rest, %s", installAdvice)
	})
	return true, nil
}

// unpinnedHostRefusal is the terminal error for a run that reserved host
// capacity on a box with nothing to reserve it from. It names the pin, the
// fix, and the escape hatch, because a run refused over infrastructure the
// operator may not know exists has to arrive with all three.
func unpinnedHostRefusal(where string) error {
	return fmt.Errorf("local admission: this pipeline reserves host capacity with %s, but no admission daemon is running "+
		"and no sparkwing is installed to host one, so nothing can hold that reservation. "+
		"To honor it, %s. "+
		"To run without it -- host CPU and memory unarbitrated, so concurrent runs on this box can oversubscribe it -- set %s=1",
		where, installAdvice, AllowUnadmittedEnv)
}

// staleDaemonRefusal is the terminal error for a live daemon this client
// cannot speak to. It leads with the client's own sentence, which already
// names both versions and the release to install, and adds only what that
// sentence cannot know: that something is arbitrating this box, so running
// anyway is not free.
func staleDaemonRefusal(err error) error {
	return fmt.Errorf("local admission: %w. That daemon is arbitrating this box now, so running without it would "+
		"oversubscribe the machine rather than merely go uncoordinated. "+
		"To run anyway, set %s=1", err, AllowUnadmittedEnv)
}
