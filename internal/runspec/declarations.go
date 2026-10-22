package runspec

import (
	"errors"
	"maps"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// DeclarationGate owns discovery completeness and the registration of declarations.
// Diagnostics remain private until every pipeline has supplied its declarations.
type DeclarationGate struct {
	doc      nodeproto.DescribeDoc
	masker   *secrets.Masker
	complete bool
	defaults []SecretValue
}

// DeclaredArguments is the resolved argument set and its sealing projection.
type DeclaredArguments struct {
	Description  nodeproto.PipelineDescription
	Args         map[string]string
	SecretArgs   []string
	Registration SecretRegistration
}

func NewDeclarationGate(doc nodeproto.DescribeDoc, masker *secrets.Masker) (*DeclarationGate, error) {
	if doc.Pipelines == nil || masker == nil {
		return nil, declarationError("SDK describe requires a pipelines array and host masker")
	}
	g := &DeclarationGate{doc: doc, masker: masker, complete: true}
	seen := map[string]bool{}
	for _, pipeline := range doc.Pipelines {
		if pipeline.Name == "" || seen[pipeline.Name] {
			return nil, declarationError("SDK describe has invalid pipeline declarations")
		}
		seen[pipeline.Name] = true
		if pipeline.Error != nil {
			g.complete = false
			continue
		}
		if pipeline.Args == nil {
			return nil, declarationError("SDK describe has invalid pipeline declarations")
		}
		args := map[string]bool{}
		for _, argument := range pipeline.Args {
			if argument.Name == "" || args[argument.Name] {
				return nil, declarationError("SDK describe has ambiguous arguments")
			}
			args[argument.Name] = true
			// Unselected defaults protect discovery output but cannot refuse another pipeline.
			if argument.Secret && argument.Default != nil && *argument.Default != "" {
				for _, value := range secretCandidates(*argument.Default, argument.Type == "[]string") {
					if err := masker.Register(value); err == nil {
						g.defaults = append(g.defaults, SecretValue{Raw: value})
					}
				}
			}
		}
	}
	return g, nil
}

func (g *DeclarationGate) Complete() bool { return g != nil && g.complete }

func (g *DeclarationGate) Resolve(name string, args map[string]string, secretArgs []string) (DeclaredArguments, error) {
	pipeline, ok := g.doc.Pipeline(name)
	if !ok {
		return DeclaredArguments{}, &apierr.Error{Code: apierr.UnknownPipeline, Message: "SDK describe does not declare the requested pipeline"}
	}
	if pipeline.Error != nil {
		return DeclaredArguments{}, g.Error(pipeline.Error)
	}
	resolved, err := RegisterArguments(pipeline.Args, args, secretArgs, g.masker)
	if err != nil {
		return resolved, err
	}
	resolved.Registration.Defaults = append(resolved.Registration.Defaults, g.defaults...)
	resolved.Registration, err = BuildSecretRegistration(Request{RequestedArgs: args, SecretArgs: Optional[[]string]{Set: true, Value: secretArgs}}, Resolved{Args: resolved.Args, SecretArgs: resolved.SecretArgs, SecretRegistration: resolved.Registration})
	resolved.Description = pipeline
	return resolved, err
}

// RegisterArguments is the argument registration projection for every host entrypoint.
func RegisterArguments(arguments []nodeproto.ArgumentDescription, args map[string]string, secretArgs []string, masker *secrets.Masker) (DeclaredArguments, error) {
	resolved, err := projectArguments(arguments, args, secretArgs)
	if err != nil {
		return resolved, err
	}
	for _, value := range resolved.Registration.Values {
		if err := masker.Register(value); err != nil {
			return DeclaredArguments{}, err
		}
	}
	return resolved, nil
}

func projectArguments(arguments []nodeproto.ArgumentDescription, args map[string]string, secretArgs []string) (DeclaredArguments, error) {
	resolved := DeclaredArguments{Args: maps.Clone(args), SecretArgs: append([]string{}, secretArgs...)}
	if resolved.Args == nil {
		resolved.Args = map[string]string{}
	}
	projection := SecretRegistration{}
	for _, argument := range arguments {
		if _, present := resolved.Args[argument.Name]; !present && argument.Default != nil && *argument.Default != "" {
			resolved.Args[argument.Name] = *argument.Default
		}
		if argument.Type == "[]string" {
			projection.ListArgs = append(projection.ListArgs, argument.Name)
		}
		if argument.Secret || slices.Contains(secretArgs, argument.Name) {
			if argument.Default != nil && *argument.Default != "" {
				projection.Defaults = append(projection.Defaults, SecretValue{Raw: *argument.Default, List: argument.Type == "[]string"})
			}
			if _, present := resolved.Args[argument.Name]; present && !slices.Contains(resolved.SecretArgs, argument.Name) {
				resolved.SecretArgs = append(resolved.SecretArgs, argument.Name)
			}
		}
	}
	registration, err := BuildSecretRegistration(Request{RequestedArgs: args, SecretArgs: Optional[[]string]{Set: true, Value: secretArgs}}, Resolved{Args: resolved.Args, SecretArgs: resolved.SecretArgs, SecretRegistration: projection})
	if err != nil {
		return DeclaredArguments{}, err
	}
	resolved.Registration = registration
	return resolved, nil
}

func declarationError(message string) error {
	return &apierr.Error{Code: apierr.ProtocolError, Message: message}
}

// Error withholds unclassified author errors and sanitizes classified payloads.
func (g *DeclarationGate) Error(err error) error {
	if errors.Is(err, secrets.ErrInvalidValue) {
		return secrets.ErrInvalidValue
	}
	if !g.Complete() {
		code := apierr.SDKError
		var canonical *apierr.Error
		if errors.As(err, &canonical) && slices.ContainsFunc(apierr.Codes, func(definition apierr.Definition) bool { return definition.Code == canonical.Code }) {
			code = canonical.Code
		}
		return &apierr.Error{Code: code, Message: "pipeline discovery failed (details withheld: secrets not yet known)"}
	}
	var canonical *apierr.Error
	if errors.As(err, &canonical) {
		return g.masker.Error(canonical)
	}
	return &apierr.Error{Code: apierr.SDKError, Message: g.masker.Mask(err.Error())}
}

// PipelinePresentation is sanitized metadata for display, never execution routing.
type PipelinePresentation nodeproto.PipelineDescription

// DescribePresentation contains public discovery metadata safe for display and caching.
type DescribePresentation struct {
	Pipelines []PipelinePresentation `json:"pipelines"`
}

// Declarations returns private metadata for pipeline lookup and argument parsing.
func (g *DeclarationGate) Declarations() nodeproto.DescribeDoc {
	doc := nodeproto.DescribeDoc{Pipelines: slices.Clone(g.doc.Pipelines)}
	for i := range doc.Pipelines {
		if doc.Pipelines[i].Error != nil {
			doc.Pipelines[i].Error = g.Error(doc.Pipelines[i].Error).(*apierr.Error)
		}
	}
	return doc
}

// Presentation withholds all author text until every declaration is known.
func (g *DeclarationGate) Presentation() DescribePresentation {
	doc := DescribePresentation{Pipelines: make([]PipelinePresentation, len(g.doc.Pipelines))}
	for i, source := range g.doc.Pipelines {
		pipeline := source
		pipeline.Name = g.masker.Mask(source.Name)
		pipeline.Args = slices.Clone(source.Args)
		if source.Error != nil {
			pipeline.Error = g.Error(source.Error).(*apierr.Error)
		}
		if !g.Complete() {
			doc.Pipelines[i] = PipelinePresentation{Args: []nodeproto.ArgumentDescription{}, Error: &apierr.Error{Code: apierr.SDKError, Message: "pipeline discovery incomplete (details withheld: secrets not yet known)"}}
			continue
		}
		pipeline.Help, pipeline.Short = g.masker.Mask(source.Help), g.masker.Mask(source.Short)
		pipeline.Examples = slices.Clone(source.Examples)
		for j := range pipeline.Examples {
			pipeline.Examples[j].Command = g.masker.Mask(pipeline.Examples[j].Command)
			pipeline.Examples[j].Comment = g.masker.Mask(pipeline.Examples[j].Comment)
		}
		pipeline.EnvVars = slices.Clone(source.EnvVars)
		for j := range pipeline.EnvVars {
			env := &pipeline.EnvVars[j]
			env.Name, env.Description = g.masker.Mask(env.Name), g.masker.Mask(env.Description)
			env.Default = maskDefault(g.masker, env.Default)
		}
		pipeline.Risks = maskStrings(g.masker, source.Risks)
		pipeline.RisksByStep = slices.Clone(source.RisksByStep)
		for j := range pipeline.RisksByStep {
			risk := &pipeline.RisksByStep[j]
			risk.JobID, risk.StepID = g.masker.Mask(risk.JobID), g.masker.Mask(risk.StepID)
			risk.Labels = maskStrings(g.masker, risk.Labels)
		}
		pipeline.Secrets = slices.Clone(source.Secrets)
		for j := range pipeline.Secrets {
			pipeline.Secrets[j].Name = g.masker.Mask(pipeline.Secrets[j].Name)
		}
		for j := range pipeline.Args {
			argument := &pipeline.Args[j]
			argument.Type = g.masker.Mask(argument.Type)
			argument.Name, argument.Short, argument.JobID = g.masker.Mask(argument.Name), g.masker.Mask(argument.Short), g.masker.Mask(argument.JobID)
			argument.Description = g.masker.Mask(source.Args[j].Description)
			argument.Default = maskDefault(g.masker, source.Args[j].Default)
			argument.Enum = nil
			if argument.Secret && source.Args[j].Default != nil && *source.Args[j].Default != "" {
				hidden := "***"
				argument.Default = &hidden
			}
			if !argument.Secret {
				argument.Enum = maskStrings(g.masker, source.Args[j].Enum)
			}
		}
		doc.Pipelines[i] = PipelinePresentation(pipeline)
	}
	return doc
}

func maskDefault(masker *secrets.Masker, value *string) *string {
	if value == nil {
		return nil
	}
	masked := masker.Mask(*value)
	return &masked
}

func maskStrings(masker *secrets.Masker, values []string) []string {
	masked := slices.Clone(values)
	for i := range masked {
		masked[i] = masker.Mask(masked[i])
	}
	return masked
}
