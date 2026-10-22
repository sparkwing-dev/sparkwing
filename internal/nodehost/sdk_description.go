package nodehost

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func (p SDKPlanner) describe(raw json.RawMessage, input PlanningProcessInput) (PreparedPlanning, bool, error) {
	var doc nodeproto.DescribeDoc
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Pipelines == nil {
		return PreparedPlanning{}, false, executorError(apierr.ProtocolError, "SDK describe requires a pipelines array")
	}
	complete := true
	seen := map[string]bool{}
	for i := range doc.Pipelines {
		pipeline := &doc.Pipelines[i]
		if pipeline.Name == "" || seen[pipeline.Name] {
			return PreparedPlanning{}, false, executorError(apierr.ProtocolError, "SDK describe has invalid pipeline declarations")
		}
		seen[pipeline.Name] = true
		if pipeline.Error != nil {
			complete = false
			continue
		}
		if pipeline.Args == nil {
			return PreparedPlanning{}, false, executorError(apierr.ProtocolError, "SDK describe has invalid pipeline declarations")
		}
	}
	selected, found := (nodeproto.DescribeDoc{Pipelines: doc.Pipelines}).Pipeline(input.Entrypoint)
	if !found {
		return PreparedPlanning{}, false, executorError(apierr.UnknownPipeline, "SDK describe does not declare the requested pipeline")
	}
	if selected.Error != nil {
		return PreparedPlanning{Description: nodeproto.PipelineDescription{Name: selected.Name, Error: selected.Error}}, false, nil
	}
	if err := nodeproto.ValidateArgumentDeclarations(selected.Args); err != nil {
		return PreparedPlanning{}, false, executorError(apierr.InvalidArgs, err.Error())
	}
	if err := nodeproto.ValidateSecretDeclarations(selected.Secrets); err != nil {
		return PreparedPlanning{}, false, executorError(apierr.ProtocolError, err.Error())
	}

	args, err := nodeproto.ResolveArguments(selected, input.Args)
	if err != nil {
		return PreparedPlanning{}, false, executorError(apierr.InvalidArgs, err.Error())
	}
	prepared := PreparedPlanning{Description: selected, Args: args, SecretArgs: append([]string{}, input.SecretArgs...)}
	for _, arg := range selected.Args {
		if arg.Secret && !slices.Contains(prepared.SecretArgs, arg.Name) {
			prepared.SecretArgs = append(prepared.SecretArgs, arg.Name)
		}
	}
	if err := p.Masks.initialize(runspec.SealedRequest{Request: runspec.Request{RequestedArgs: input.Args, SecretArgs: runspec.Optional[[]string]{Set: true, Value: input.SecretArgs}}}, &runspec.Resolved{Args: prepared.Args, SecretArgs: prepared.SecretArgs}, nil, nil); err != nil {
		return PreparedPlanning{}, false, err
	}
	return prepared, complete, nil
}

type planningSecrets struct {
	prior  nodeproto.SecretMatcher
	masker *secrets.Masker
}

func (s planningSecrets) ContainsSecret(identifier string) bool {
	if s.prior != nil && s.prior.ContainsSecret(identifier) {
		return true
	}
	for _, value := range s.masker.Values() {
		if value != "" && strings.Contains(identifier, value) {
			return true
		}
	}
	return false
}
