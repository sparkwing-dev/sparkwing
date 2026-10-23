package nodehost

import (
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func (p SDKPlanner) describe(raw json.RawMessage, input PlanningProcessInput, declarationsOnly bool) (PreparedPlanning, bool, error) {
	var doc nodeproto.DescribeDoc
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Pipelines == nil {
		return PreparedPlanning{}, false, executorError(apierr.ProtocolError, "SDK describe requires a pipelines array")
	}
	gate, err := runspec.NewDeclarationGate(doc, p.Masker)
	if err != nil {
		return PreparedPlanning{}, false, err
	}
	var arguments runspec.DeclaredArguments
	if declarationsOnly {
		arguments, err = gate.Inspect(input.Entrypoint)
	} else {
		arguments, err = gate.Resolve(input.Entrypoint, input.Args, input.SecretArgs)
	}
	if err != nil {
		return PreparedPlanning{Declarations: gate}, gate.Complete() && arguments.Registration.Version != 0, gate.Error(err)
	}
	prepared := PreparedPlanning{Declarations: gate, Description: arguments.Description, Args: arguments.Args, SecretArgs: arguments.SecretArgs, SecretRegistration: arguments.Registration}

	return prepared, gate.Complete(), nil
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
