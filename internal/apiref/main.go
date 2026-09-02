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
	logs := filepath.Join(root, "pkg", "logs", "server.go")

	scopes, err := apiroutes.Scopes(authsrc, logs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apiref:", err)
		os.Exit(2)
	}

	var b strings.Builder
	b.WriteString("<!-- GENERATED from the route registrations in pkg/controller/server.go and pkg/logs/server.go by internal/apiref. Do not edit by hand; regenerate with `bash bin/gen-api-docs.sh`. -->\n")
	b.WriteString("# HTTP API reference\n\n")
	b.WriteString("Every route the controller and logs service register, with the " +
		"scope each requires, generated from the routing code. All paths are under " +
		"the `/api/v1` base (webhook and `/metrics` excepted). Scope enforcement and " +
		"the token model are in [auth.md](auth.md); `admin` is the superset that " +
		"satisfies any scope check. `public` routes run with no bearer check (the " +
		"GitHub webhook is HMAC-verified instead); `authenticated` routes take any " +
		"valid bearer and check no further scope.\n\n")

	writeRoutes(&b, "Controller", scopes, controller)
	writeRoutes(&b, "Logs service", scopes, logs)

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
