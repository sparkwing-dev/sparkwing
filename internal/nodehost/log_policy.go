package nodehost

import hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"

import (
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func acceptedDebug(accepted AcceptedRun) (bool, error) {
	var spec runspec.Spec
	if json.Unmarshal(accepted.Spec, &spec) != nil {
		return false, executorError(hosterr.SpecDrift, "log policy requires an accepted run")
	}
	return spec.Resolved.Settings.Options.Debug.Value, nil
}
