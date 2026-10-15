package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: apiref")
		os.Exit(2)
	}
	if err := writeReference(os.Stdout, capability.Routes); err != nil {
		fmt.Fprintln(os.Stderr, "apiref:", err)
		os.Exit(1)
	}
}

func writeReference(out io.Writer, routes []capability.RoutePolicy) error {
	var b strings.Builder
	b.WriteString("<!-- GENERATED from internal/capability.Routes by internal/apiref. Do not edit by hand; regenerate with go run ./internal/apiref > docs/api-reference.md. -->\n")
	b.WriteString("# HTTP API reference\n\n<!-- design:begin -->\n\n")
	b.WriteString("The shared capability policy defines the controller HTTP and local API socket contract. Each row retains its audience, ownership lookup, authentication, retry policy and complete access alternatives. See [Controller HTTP API](api.md) for request and response contracts and [Authentication](auth.md) for credential issuance. Native controller bindings require separate integration evidence.\n\n")
	b.WriteString("An access alternative is `credential / grants / binding / liveness`. Every grant joined with `+` is required. Alternatives separated by a line break stand alone. Grants, bindings and liveness exceptions cannot be combined across alternatives. External authentication has no capability alternative.\n\n")
	b.WriteString("The owner names the server lookup. Bindings and current authority constrain every caller, including deployment administrators and local peers. An operator-console session is separate from a deployment-admin capability. `safe`, `exact_body` and `idempotency_key` identify eligible transport retries. A client may further refuse replay when a response carries one-time authority. `never` forbids automatic replay.\n\n")
	b.WriteString("| Method | Path | Audience | Owner | Authentication | Retry | Access alternatives |\n|---|---|---|---|---|---|---|\n")
	for _, route := range routes {
		access := []string{}
		for _, alternative := range route.Access {
			grants := []string{}
			for _, grant := range alternative.Grants {
				grants = append(grants, string(grant))
			}
			access = append(access, fmt.Sprintf("`%s` / `%s` / `%s` / `%s`", alternative.Credential, strings.Join(grants, " + "), alternative.Binding, alternative.Liveness))
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | %s |\n", route.Method, route.Path, route.Audience, route.Owner, route.Authentication, route.Retry, strings.Join(access, "<br>"))
	}
	b.WriteString("\n<!-- design:end -->\n")
	n, err := io.WriteString(out, b.String())
	if err == nil && n != b.Len() {
		return io.ErrShortWrite
	}
	return err
}
