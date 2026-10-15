package nodehost

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func (h *sdkCode) generate(ctx context.Context, session *executorSession, code NodeInput, job nodeproto.JobSpec, output json.RawMessage) (json.RawMessage, error) {
	jobs := []json.RawMessage{}
	for _, id := range job.Closures.Generate {
		value, err := h.eval(ctx, session, code, id, output, nil)
		if err != nil {
			return nil, err
		}
		value = bytes.TrimSpace(value)
		if len(value) == 0 || value[0] != '[' {
			return nil, executorError(apierr.ProtocolError, "generator must return a jobs array")
		}
		var group []json.RawMessage
		if err := json.Unmarshal(value, &group); err != nil {
			return nil, executorError(apierr.ProtocolError, "generator returned an invalid jobs array")
		}
		for _, raw := range group {
			var member struct {
				Groups []string `json:"groups"`
			}
			if err := json.Unmarshal(raw, &member); err != nil || !executorContains(member.Groups, id) {
				return nil, executorError(apierr.PlanInvalid, "generated job does not belong to its evaluated generator")
			}
		}
		jobs = append(jobs, group...)
	}
	// safety: the shared validator must see unknown fields and edges between generator groups together.
	return json.Marshal(struct {
		Jobs []json.RawMessage `json:"jobs"`
	}{Jobs: jobs})
}
