package nodehost

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func (h *sdkCode) generate(ctx context.Context, session *executorSession, code NodeInput, job nodeproto.JobSpec, output json.RawMessage) (json.RawMessage, error) {
	if err := session.prepareArguments(job); err != nil {
		return nil, err
	}
	jobs := []json.RawMessage{}
	for _, id := range job.Closures.Generate {
		value, err := h.eval(ctx, session, code, id, "generate", output, nil)
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
				Groups    []string                        `json:"groups"`
				Arguments []nodeproto.ArgumentDescription `json:"arguments"`
			}
			if err := json.Unmarshal(raw, &member); err != nil || !executorContains(member.Groups, id) {
				return nil, executorError(apierr.PlanInvalid, "generated job does not belong to its evaluated generator")
			}
			if _, err := nodeproto.PrepareJobArguments(member.Arguments, session.rawArgs); err != nil {
				return nil, err
			}
		}
		jobs = append(jobs, group...)
	}
	// safety: the shared validator must see unknown fields and edges between generator groups together.
	return json.Marshal(struct {
		Jobs []json.RawMessage `json:"jobs"`
	}{Jobs: jobs})
}
