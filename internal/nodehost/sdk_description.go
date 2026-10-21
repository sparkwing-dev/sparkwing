package nodehost

import (
	"encoding/json"
	"maps"
	"slices"

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
	registration := runspec.SecretRegistration{}
	for _, pipeline := range doc.Pipelines {
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
		args := map[string]bool{}
		for _, arg := range pipeline.Args {
			if arg.Name == "" || args[arg.Name] {
				return PreparedPlanning{}, false, executorError(apierr.ProtocolError, "SDK describe has ambiguous arguments")
			}
			args[arg.Name] = true
			if arg.Secret && arg.Default != nil {
				registration.Defaults = append(registration.Defaults, runspec.SecretValue{Raw: *arg.Default, List: arg.Type == "[]string"})
			}
		}
	}
	selected, found := (nodeproto.DescribeDoc{Pipelines: doc.Pipelines}).Pipeline(input.Entrypoint)
	if !found {
		return PreparedPlanning{}, false, executorError(apierr.UnknownPipeline, "SDK describe does not declare the requested pipeline")
	}
	if selected.Error != nil {
		return PreparedPlanning{Description: nodeproto.PipelineDescription{Name: selected.Name, Error: selected.Error}}, false, nil
	}
	prepared := PreparedPlanning{Description: selected, Args: maps.Clone(input.Args), SecretArgs: append([]string{}, input.SecretArgs...)}
	if prepared.Args == nil {
		prepared.Args = map[string]string{}
	}
	for _, arg := range selected.Args {
		if _, present := prepared.Args[arg.Name]; !present && arg.Default != nil {
			prepared.Args[arg.Name] = *arg.Default
		}
		if arg.Type == "[]string" {
			registration.ListArgs = append(registration.ListArgs, arg.Name)
		}
		if arg.Secret || slices.Contains(input.SecretArgs, arg.Name) {
			if arg.Default != nil {
				registration.Defaults = append(registration.Defaults, runspec.SecretValue{Raw: *arg.Default, List: arg.Type == "[]string"})
			}
			if !slices.Contains(prepared.SecretArgs, arg.Name) {
				prepared.SecretArgs = append(prepared.SecretArgs, arg.Name)
			}
		}
	}
	resolved := runspec.Resolved{Args: prepared.Args, SecretArgs: prepared.SecretArgs, SecretRegistration: registration}
	projection, err := runspec.BuildSecretRegistration(runspec.Request{RequestedArgs: input.Args, SecretArgs: runspec.Optional[[]string]{Set: true, Value: input.SecretArgs}}, resolved)
	if err != nil {
		return PreparedPlanning{}, false, err
	}
	prepared.SecretRegistration = projection
	for _, value := range projection.Values {
		if err := p.Masks.Register(value); err != nil {
			return PreparedPlanning{}, false, err
		}
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
	return s.masker.ContainsSecret(identifier)
}
