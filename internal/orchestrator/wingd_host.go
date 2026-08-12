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

// planDeclaresLocalAdmission reports whether a run explicitly asked the
// local admission daemon for something, and names what, for the error
// that fails such a run closed.
//
// A plan declares explicitly when it carries at least one of:
//
//   - a plan-level .Resources() pin -- [sparkwing.Plan.ResourceHints] with
//     a non-zero core or memory figure;
//   - a node-level .Resources() pin on any node in the plan;
//   - a plan-level .Concurrency() group the local daemon arbitrates, which
//     is box or run scope ([groupUsesLocalDaemon]); global-scope groups
//     pool across the fleet through the shared store and never reach the
//     daemon, so they are not a local-daemon declaration;
//   - a node-level .Concurrency() group with such a scope.
//
// Everything else is an implicit default reservation. A run that declared
// nothing is still charged host cost per node -- from its measured
// profile, else a conservative cold-start figure -- but that charge is a
// courtesy the daemon supplies, not something the pipeline author asked
// for, and it is the only thing lost when the run proceeds unadmitted. A
// declaration, by contrast, is the author stating a correctness
// requirement ("no two of these at once", "this needs 8 cores"), which
// silently dropping would violate.
func planDeclaresLocalAdmission(plan *sparkwing.Plan) (declared bool, why string) {
	if plan == nil {
		return false, ""
	}
	var reasons []string
	if h := plan.ResourceHints(); h != nil && (h.Cores > 0 || h.MemoryBytes > 0) {
		reasons = append(reasons, "a plan-level .Resources() pin")
	}
	groups := map[string]bool{}
	for _, membership := range plan.PlanConcurrency() {
		if g := membership.Group; g != nil && groupUsesLocalDaemon(g) {
			groups[g.Name()] = true
		}
	}
	pinnedNode := false
	for _, n := range plan.Nodes() {
		if h := n.ResourceHints(); h != nil && (h.Cores > 0 || h.MemoryBytes > 0) {
			pinnedNode = true
		}
		if g := n.ConcurrencyGroupRef(); g != nil && groupUsesLocalDaemon(g) {
			groups[g.Name()] = true
		}
	}
	if pinnedNode {
		reasons = append(reasons, "a node-level .Resources() pin")
	}
	if len(groups) > 0 {
		names := make([]string, 0, len(groups))
		for name := range groups {
			names = append(names, strconv.Quote(name))
		}
		sort.Strings(names)
		noun := "concurrency group"
		if len(names) > 1 {
			noun = "concurrency groups"
		}
		reasons = append(reasons, noun+" "+strings.Join(names, ", "))
	}
	if len(reasons) == 0 {
		return false, ""
	}
	return true, strings.Join(reasons, " and ")
}

// allowUnadmitted reports whether the operator has authorized a
// declaring run to proceed uncoordinated. Any non-empty value other than
// the explicit falses counts, matching how the rest of the tree reads its
// boolean environment switches.
func allowUnadmitted() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(AllowUnadmittedEnv))) {
	case "", "0", "false", "no":
		return false
	default:
		return true
	}
}

// unhostedOutcome classifies a run-start admission failure for a client
// that cannot host the daemon, splitting on what the pipeline declared.
//
// It answers only the two conditions that mean "this box offers no
// admission this client can use, and no action of this client's can
// change that":
//
//   - [wingdclient.ErrNoDaemonHost] -- nothing is listening and no
//     installed sparkwing exists to start one. It is reached only after a
//     dial failure meaning absence; a socket that could not be reached at
//     all reports unreachable instead and stays a hard error for
//     everyone, because a daemon may be arbitrating other runs behind it
//     and joining them uncoordinated oversubscribes the box.
//   - [wingdclient.ErrDaemonTooOld] -- a daemon answered but speaks an
//     older protocol major than this client, and this client may not
//     replace it.
//
// A run whose plan declares resource claims or concurrency groups fails
// closed on either: the declaration is a correctness requirement, and
// dropping it silently would let two runs the author said must not
// overlap overlap. A run with only implicit default reservations degrades
// to unadmitted with one warning, which is what keeps a standalone
// pipeline binary on a machine with no sparkwing installed a working
// product. [AllowUnadmittedEnv] forces the degrade for a declaring run.
//
// Every other failure -- unreachable, exhausted takeover, a policy
// rejection, a queue timeout -- returns (false, nil) and the caller keeps
// the original error.
func (la *LocalAdmission) unhostedOutcome(err error, plan *sparkwing.Plan) (degrade bool, failure error) {
	if la == nil || !la.PipelineClient || err == nil {
		return false, nil
	}
	noHost := errors.Is(err, wingdclient.ErrNoDaemonHost)
	tooOld := errors.Is(err, wingdclient.ErrDaemonTooOld)
	if !noHost && !tooOld {
		return false, nil
	}
	declared, why := planDeclaresLocalAdmission(plan)
	if declared && !allowUnadmitted() {
		return false, unadmittedRefusal(err, why, tooOld)
	}
	la.unadmittedOnce.Do(func() {
		if tooOld {
			la.logf("%v; running without local coordination", err)
			return
		}
		la.logf("no admission daemon is running and no sparkwing is installed to host one; " +
			"running without local coordination -- concurrent runs on this box will not queue against each other. " +
			"To coordinate them, " + installAdvice)
	})
	return true, nil
}

// unadmittedRefusal is the terminal error for a run that declared what it
// needs from the local daemon on a box that cannot provide it. It names
// the declaration, the fix, and the escape hatch, because a run refused
// for infrastructure the operator may not know exists has to arrive with
// all three.
func unadmittedRefusal(err error, why string, tooOld bool) error {
	cause := "no admission daemon is running and no sparkwing is installed to host one"
	if tooOld {
		cause = err.Error()
	}
	return fmt.Errorf("local admission: this pipeline declares %s, which the local admission daemon arbitrates, but %s. "+
		"To coordinate this run, %s. "+
		"To run it uncoordinated anyway -- concurrent runs on this box will not queue against each other -- set %s=1",
		why, cause, installAdvice, AllowUnadmittedEnv)
}
