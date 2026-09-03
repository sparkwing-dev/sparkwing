package orchestrator

import (
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

// safety: age never refuses a run. Every sentinel here is a gap the run
// already answered by choosing the standalone store and printing one block,
// so admission proceeds unadmitted without adding a second line for it.
func (la *LocalAdmission) unhostedOutcome(err error) bool {
	if la == nil || !la.PipelineClient {
		return false
	}
	return standaloneReasonFor(err) != ""
}
