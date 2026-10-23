package nodehost

import (
	"context"
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

const generatorFailureMessage = "generator failed (details withheld: secret declarations unavailable)"

type declarationProcess interface {
	gateDeclarations()
	releaseDeclarations(context.Context, runspec.Spec, nodeproto.EvalResult) error
}

func (s *protocolSession) gateDeclarations() {
	if s.cfg.output != nil {
		s.cfg.output.gate()
	}
}

func (s *protocolSession) releaseDeclarations(ctx context.Context, spec runspec.Spec, result nodeproto.EvalResult) error {
	if s.cfg.output == nil || s.cfg.Masks == nil {
		return executorError(apierr.Internal, "generator output requires host masking")
	}
	if result.Error != nil || !result.DeclarationsKnown {
		return executorError(apierr.SDKError, generatorFailureMessage)
	}
	projection, err := generatorRegistration(spec, result.Arguments)
	if err != nil {
		return err
	}
	for _, value := range projection.Values {
		if err := s.cfg.Masks.registerValue(value); err != nil {
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
	return runspec.GeneratedRegistration(spec.Request.Request, spec.Resolved, arguments)
}
