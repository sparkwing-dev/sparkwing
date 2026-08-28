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

const AllowUnadmittedEnv = "SPARKWING_ALLOW_UNADMITTED"

const installAdvice = "install or update the sparkwing CLI on this host, " +
	"or set " + wingdclient.HostBinEnv + " to an installed sparkwing binary"

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

func allowUnadmitted() bool {
	return os.Getenv(AllowUnadmittedEnv) == "1"
}

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

func unpinnedHostRefusal(where string) error {
	return fmt.Errorf("local admission: this pipeline reserves host capacity with %s, but no admission daemon is running "+
		"and no sparkwing is installed to host one, so nothing can hold that reservation. "+
		"To honor it, %s. "+
		"To run without it -- host CPU and memory unarbitrated, so concurrent runs on this box can oversubscribe it -- set %s=1",
		where, installAdvice, AllowUnadmittedEnv)
}

func staleDaemonRefusal(err error) error {
	return fmt.Errorf("local admission: %w. That daemon is arbitrating this box now, so running without it would "+
		"oversubscribe the machine rather than merely go uncoordinated. "+
		"To run anyway, set %s=1", err, AllowUnadmittedEnv)
}
