package orchestrator

import (
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
)

// safety: the runtime that found the miss cannot see which store the run reads,
// so the fix for each secret is named here, where that store is known.
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
	if opts.localSecrets {
		return "this machine's local secret store"
	}
	return "the controller"
}

func secretFix(opts Options, name, pipeline string) string {
	if opts.localSecrets {
		return "sparkwing secrets set --name " + name + " --file <path>"
	}
	return fmt.Sprintf("sparkwing secrets set --name %s --file <path> --pipeline %s --profile <profile> "+
		"(or --shared instead of --pipeline for every pipeline)", name, pipeline)
}
