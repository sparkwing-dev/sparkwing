package nodehost

import (
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func acceptedDebug(accepted AcceptedRun) (bool, error) {
	var spec runspec.Spec
	if json.Unmarshal(accepted.Spec, &spec) != nil {
		return false, executorError(apierr.SpecDrift, "log policy requires an accepted run")
	}
	return spec.Resolved.Settings.Options.Debug.Value, nil
}
