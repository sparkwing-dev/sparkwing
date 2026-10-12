package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func selectSecretResolver(ctx context.Context, opts Options) (secrets.Source, error) {
	spec := effectiveSecretsSpec(opts)
	if spec == nil {
		return nil, nil
	}
	if spec.Type == backends.TypeController && spec.URL != "" && opts.RunID != "" {
		c := client.NewWithToken(strings.TrimRight(spec.URL, "/"), http.DefaultClient, spec.ResolvedToken())
		return controllerSecretSource(ctx, c, opts.RunID), nil
	}
	resolver, err := secretsource.FromSpec(*spec)
	if err != nil {
		return nil, err
	}
	return resolverAsSource(ctx, resolver), nil
}

func effectiveSecretsSpec(opts Options) *backends.Spec {
	if opts.LocalOnly || opts.Profile == nil {
		return nil
	}
	return opts.Profile.Surfaces().Secrets
}

func resolverAsSource(ctx context.Context, r secretsource.Resolver) secrets.Source {
	return secrets.SourceFunc(func(name string) (string, bool, error) {
		return r.Resolve(ctx, name)
	})
}

// safety: the runtime that found the miss cannot see which store the run reads,
// so the fix for each secret is named here, where the profile is known.
func secretPreflightError(missing *sparkwingruntime.MissingSecretsError, opts Options) error {
	noun := "secrets"
	if len(missing.Names) == 1 {
		noun = "secret"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "pipeline %q did not start: it declares %d required %s that %s does not hold.",
		missing.Pipeline, len(missing.Names), noun, secretStoreName(opts))
	for _, name := range missing.Names {
		fmt.Fprintf(&b, "\n- %s\n  Fix: %s", name, secretFix(opts, name, missing.Pipeline))
	}
	b.WriteString("\nIf the run can do without a secret, declare it optional: tag its field in Secrets() `sw:\"NAME,optional\"`.")
	return preflightError{text: b.String(), missing: missing}
}

type preflightError struct {
	text    string
	missing error
}

func (e preflightError) Error() string { return e.text }
func (e preflightError) Unwrap() error { return e.missing }

func secretStoreName(opts Options) string {
	spec := effectiveSecretsSpec(opts)
	switch {
	case spec == nil && opts.localSecrets:
		return "this machine's local secret store"
	case spec == nil:
		return "the controller"
	case spec.Type == backends.TypeController:
		return fmt.Sprintf("profile %q's controller", opts.Profile.Name)
	default:
		return fmt.Sprintf("profile %q's %s secrets backend", opts.Profile.Name, spec.Type)
	}
}

func secretFix(opts Options, name, pipeline string) string {
	spec := effectiveSecretsSpec(opts)
	switch {
	case spec == nil && opts.localSecrets:
		return "sparkwing secrets set --name " + name + " --file <path>"
	case spec == nil:
		return fmt.Sprintf("sparkwing secrets set --name %s --file <path> --pipeline %s --profile <profile> "+
			"(or --shared instead of --pipeline for every pipeline)", name, pipeline)
	case spec.Type == backends.TypeController:
		return fmt.Sprintf("sparkwing secrets set --name %s --file <path> --pipeline %s --profile %s", name, pipeline, opts.Profile.Name)
	case spec.Type == backends.TypeEnv:
		return fmt.Sprintf("export %s%s=<value> before the run", spec.Prefix, name)
	case spec.Type == backends.TypeFilesystem:
		return fmt.Sprintf("add a line %s=<value> to %s", name, spec.Path)
	default:
		return fmt.Sprintf("give profile %q a secrets backend that holds %s", opts.Profile.Name, name)
	}
}
