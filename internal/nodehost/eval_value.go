package nodehost

import (
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func evalValue(result nodewire.EvalResult, kind string) (json.RawMessage, error) {
	if result.Error != nil {
		return nil, result.Error
	}
	if !json.Valid(result.Value) {
		return nil, executorError(apierr.ProtocolError, "SDK closure returned invalid JSON")
	}
	if err := nodeproto.ValidateEvalValue(result, kind); err != nil {
		return nil, executorError(apierr.ProtocolError, "SDK closure returned an invalid value")
	}
	return result.Value, nil
}
