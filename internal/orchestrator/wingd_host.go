package orchestrator

import (
	"errors"
	"os"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const AllowUnadmittedEnv = "SPARKWING_ALLOW_UNADMITTED"

const installAdvice = "curl -fsSL https://sparkwing.dev/install.sh | sh"

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

func allowUnadmitted() bool {
	return os.Getenv(AllowUnadmittedEnv) == "1"
}

// unhosted describes what a run already decided about its store, so admission
// can tell a gap the run answered from one it is meeting for the first time.
type unhosted struct {
	reason string
	skew   bool
	hosted bool
}

// safety: age never refuses a run, and the reasons here are the same set the
// store choice degrades on. A run whose store choice already printed a block
// stays quiet; one reaching a gap the block never covered says so once,
// because otherwise coordination disappears with nothing on stderr.
func (la *LocalAdmission) unhostedOutcome(err error, state unhosted) bool {
	if la == nil || !la.PipelineClient {
		return false
	}
	reason := standaloneReasonFor(err)
	if reason == "" && !la.storeSkewRefusal(err, state) {
		return false
	}
	if state.reason == "" {
		la.announceUnadmitted(err, state.hosted)
	}
	return true
}

// safety: the daemon whose store skew sent this run standalone answers
// admission with that same store error. Gated on the skew rather than on the
// whole branch so a corrupt store on a daemon serving no api.sock still
// refuses, which is the one refusal the design reserves.
func (la *LocalAdmission) storeSkewRefusal(err error, state unhosted) bool {
	if !state.skew || state.reason != standaloneDaemonOlder {
		return false
	}
	var admErr *wingdclient.AdmissionError
	return errors.As(err, &admErr) && admErr.Key == terminalCheckKey
}

func (la *LocalAdmission) announceUnadmitted(err error, hosted bool) {
	where := "this run keeps the store it already opened"
	if hosted {
		where = "this run's state stays with the daemon it already reached"
	}
	la.logf("%v; running without local coordination -- host CPU and memory are not arbitrated, "+
		"so concurrent runs on this box can oversubscribe it, and %s", err, where)
}
