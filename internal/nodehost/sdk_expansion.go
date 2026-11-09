package nodehost

import (
	"context"
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func (h *sdkCode) generate(ctx context.Context, session *executorSession, code NodeInput, job nodewire.JobSpec, output json.RawMessage) (json.RawMessage, error) {
	if err := session.prepareArguments(job); err != nil {
		return nil, err
	}
	jobs := []json.RawMessage{}
	for _, id := range job.Closures.Generate {
		value, err := h.eval(ctx, session, code, id, "generate", output, nil)
		if err != nil {
			return nil, err
		}

		var document map[string]json.RawMessage
		if err := json.Unmarshal(value, &document); err != nil || document == nil {
			return nil, executorError(apierr.ProtocolError, "generator must return a plan document")
		}
		for key := range document {
			if key != "jobs" {
				return nil, executorError(apierr.PlanInvalid, "expansion may declare jobs only")
			}
		}
		var group []json.RawMessage
		if err := json.Unmarshal(document["jobs"], &group); err != nil || group == nil {
			return nil, executorError(apierr.ProtocolError, "generator requires a jobs array")
		}
		for _, raw := range group {
			var member struct {
				Groups    []string                       `json:"groups"`
				Arguments []nodewire.ArgumentDescription `json:"arguments"`
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
