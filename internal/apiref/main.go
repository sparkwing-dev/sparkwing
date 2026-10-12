package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apiroutes"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: apiref <repo-root>")
		os.Exit(2)
	}
	root := os.Args[1]
	controller := filepath.Join(root, "pkg", "controller", "server.go")
	authsrc := filepath.Join(root, "pkg", "controller", "auth.go")
	scopes, err := apiroutes.Scopes(authsrc, filepath.Join(root, "pkg", "store", "identity_scopes.go"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "apiref:", err)
		os.Exit(2)
	}

	var b strings.Builder
	b.WriteString("<!-- GENERATED from the route registrations in pkg/controller/server.go by internal/apiref. Do not edit by hand; regenerate with `bash bin/gen-api-docs.sh`. -->\n")
	b.WriteString("# HTTP API reference\n\n")
	b.WriteString("Every route the controller registers, with the " +
		"scope each requires, generated from the routing code. All paths are under " +
		"the `/api/v1` base (webhook and `/metrics` excepted). Scope enforcement and " +
		"the token model are in [auth.md](auth.md); `admin` satisfies any scope " +
		"check except `credits.grant`, which only the checkout service's own token " +
		"passes. `public` routes run with no bearer check (the " +
		"GitHub webhook is HMAC-verified instead); `authenticated` routes take any " +
		"valid bearer and check no further scope. `claim` routes answer the claim " +
		"token of one node claim, and only for that claim's own run and node. " +
		"`operator` routes answer only the signed-in session of an account the " +
		"controller lists with --operator-accounts; no token reaches them.\n\n")

	writeRoutes(&b, "Controller", scopes, controller)

	fmt.Print(b.String())
}

func writeRoutes(b *strings.Builder, title string, scopes map[string]string, file string) {
	routes, err := apiroutes.Parse(file, scopes)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apiref:", err)
		os.Exit(2)
	}
	if len(routes) == 0 {
		return
	}

	b.WriteString("## " + title + "\n\n")
	b.WriteString("| Method | Path | Scope |\n|---|---|---|\n")
	for _, r := range routes {
		b.WriteString("| `" + r.Method + "` | `" + r.Path + "` | `" + r.Scope + "` |\n")
	}
	b.WriteString("\n")
}
