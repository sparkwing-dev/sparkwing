package nodehost

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type declarationProcess interface {
	gateDeclarations()
	releaseDeclarations(context.Context, runspec.Spec, nodeproto.EvalResult) error
}

func (s *ProtocolSession) gateDeclarations() {
	if s.cfg.output != nil {
		s.cfg.output.gate()
	}
}

func (s *ProtocolSession) releaseDeclarations(ctx context.Context, spec runspec.Spec, result nodeproto.EvalResult) error {
	if s.cfg.output == nil || s.cfg.Masks == nil {
		return executorError(apierr.Internal, "generator output requires host masking")
	}
	if result.Error != nil || !result.DeclarationsKnown {
		return executorError(apierr.SDKError, "generator failed (details withheld: secret declarations unavailable)")
	}
	projection, err := generatorRegistration(spec, result.Arguments)
	if err != nil {
		return err
	}
	for _, value := range projection.Values {
		if err := s.cfg.Masks.Register(value); err != nil {
			return err
		}
	}
	return s.cfg.output.release(ctx)
}

func finishGeneratorLogs(ctx context.Context, process ExecutorProcess, accepted AcceptedRun, result nodeproto.EvalResult) error {
	gate, ok := process.(declarationProcess)
	if !ok {
		return nil
	}
	var spec runspec.Spec
	if json.Unmarshal(accepted.Spec, &spec) != nil {
		return executorError(apierr.SpecDrift, "generator masking requires accepted inputs")
	}
	return gate.releaseDeclarations(ctx, spec, result)
}

func generatorRegistration(spec runspec.Spec, arguments []nodeproto.ArgumentDescription) (runspec.SecretRegistration, error) {
	projection := runspec.SecretRegistration{}
	for _, arg := range arguments {
		if !arg.Secret && !slices.Contains(spec.Resolved.SecretArgs, arg.Name) && !slices.Contains(spec.Request.Request.SecretArgs.Value, arg.Name) {
			continue
		}
		list := arg.Type == "[]string"
		if arg.Default != nil {
			projection.Defaults = append(projection.Defaults, runspec.SecretValue{Raw: *arg.Default, List: list})
		}
		if raw, ok := spec.Request.Request.RequestedArgs[arg.Name]; ok {
			projection.Defaults = append(projection.Defaults, runspec.SecretValue{Raw: raw, List: list})
		}
		if raw, ok := spec.Resolved.Args[arg.Name]; ok {
			projection.Defaults = append(projection.Defaults, runspec.SecretValue{Raw: raw, List: list})
		}
	}
	return runspec.BuildSecretRegistration(runspec.Request{}, runspec.Resolved{SecretRegistration: projection})
}
