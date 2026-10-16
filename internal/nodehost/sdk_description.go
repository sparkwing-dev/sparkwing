package nodehost

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func (p SDKPlanner) describe(raw json.RawMessage, input PlanningProcessInput) (PreparedPlanning, error) {
	var doc nodeproto.DescribeDoc
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Pipelines == nil {
		return PreparedPlanning{}, executorError(apierr.ProtocolError, "SDK describe requires a pipelines array")
	}
	seen := map[string]bool{}
	var selected *nodeproto.PipelineDescription
	for i := range doc.Pipelines {
		pipeline := &doc.Pipelines[i]
		if pipeline.Name == "" || seen[pipeline.Name] || pipeline.Args == nil {
			return PreparedPlanning{}, executorError(apierr.ProtocolError, "SDK describe has invalid pipeline declarations")
		}
		seen[pipeline.Name] = true
		args := map[string]bool{}
		defaults := runspec.Resolved{Args: map[string]string{}}
		for _, arg := range pipeline.Args {
			if arg.Name == "" || args[arg.Name] {
				return PreparedPlanning{}, executorError(apierr.ProtocolError, "SDK describe has ambiguous arguments")
			}
			args[arg.Name] = true
			if arg.Secret && arg.Default != nil {
				defaults.Args[arg.Name] = *arg.Default
				defaults.SecretArgs = append(defaults.SecretArgs, arg.Name)
			}
		}
		if err := p.Masks.initialize(runspec.SealedRequest{}, &defaults, nil, nil); err != nil {
			return PreparedPlanning{}, err
		}
		if pipeline.Name == input.Entrypoint {
			selected = pipeline
		}
	}
	if selected == nil {
		return PreparedPlanning{}, executorError(apierr.UnknownPipeline, "SDK describe does not declare the requested pipeline")
	}
	prepared := PreparedPlanning{Description: *selected, Args: maps.Clone(input.Args), SecretArgs: append([]string{}, input.SecretArgs...)}
	if prepared.Args == nil {
		prepared.Args = map[string]string{}
	}
	for _, arg := range selected.Args {
		if _, present := prepared.Args[arg.Name]; !present && arg.Default != nil {
			prepared.Args[arg.Name] = *arg.Default
		}
		if arg.Secret {
			if !slices.Contains(prepared.SecretArgs, arg.Name) {
				prepared.SecretArgs = append(prepared.SecretArgs, arg.Name)
			}
		}
	}
	if err := p.Masks.initialize(runspec.SealedRequest{Request: runspec.Request{RequestedArgs: input.Args, SecretArgs: runspec.Optional[[]string]{Set: true, Value: input.SecretArgs}}}, &runspec.Resolved{Args: prepared.Args, SecretArgs: prepared.SecretArgs}, nil, nil); err != nil {
		return PreparedPlanning{}, err
	}
	return prepared, nil
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
