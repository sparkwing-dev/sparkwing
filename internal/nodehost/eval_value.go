package nodehost

import (
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func evalValue(result nodeproto.EvalResult, kind string) (json.RawMessage, error) {
	if result.Error != nil {
		return nil, result.Error
	}
	if !json.Valid(result.Value) {
		return nil, executorError(apierr.ProtocolError, "SDK closure returned invalid JSON")
	}
	if kind != "" {
		if err := result.ValidateValue(kind); err != nil {
			return nil, executorError(apierr.ProtocolError, "SDK closure returned an invalid value")
		}
	}
	return result.Value, nil
}
