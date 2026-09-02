package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	handleRE = regexp.MustCompile(`(\w+)\.Handle(?:Func)?\("([A-Z]+) (/[^"]+)",\s*(.*)$`)

	scopeRefRE = regexp.MustCompile(`requireScope\((\w+),`)

	scopeConstRE = regexp.MustCompile(`\b([A-Za-z]\w*)\s*=\s*"([a-z][a-z.]*)"`)
)

type route struct {
	method, path, scope string
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: apiref <repo-root>")
		os.Exit(2)
	}
	root := os.Args[1]
	controller := filepath.Join(root, "pkg", "controller", "server.go")
	authsrc := filepath.Join(root, "pkg", "controller", "auth.go")
	logs := filepath.Join(root, "pkg", "logs", "server.go")

	scopes := map[string]string{}
	collectScopes(scopes, authsrc)
	collectScopes(scopes, logs)

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

func collectScopes(into map[string]string, file string) {
	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apiref read:", err)
		os.Exit(2)
	}
	for _, m := range scopeConstRE.FindAllStringSubmatch(string(data), -1) {
		if strings.HasPrefix(m[1], "Scope") || strings.HasPrefix(m[1], "scope") {
			into[m[1]] = m[2]
		}
	}
}

func writeRoutes(b *strings.Builder, title string, scopes map[string]string, file string) {
	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apiref read:", err)
		os.Exit(2)
	}
	var routes []route
	for line := range strings.SplitSeq(string(data), "\n") {
		m := handleRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		receiver, method, path, rest := m[1], m[2], m[3], m[4]
		scope := "public"
		if receiver == "mux" {
			scope = "authenticated"
		}
		if sm := scopeRefRE.FindStringSubmatch(rest); sm != nil {
			if v, ok := scopes[sm[1]]; ok {
				scope = v
			} else {
				scope = sm[1]
			}
		}
		routes = append(routes, route{method, path, scope})
	}
	if len(routes) == 0 {
		return
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].path != routes[j].path {
			return routes[i].path < routes[j].path
		}
		return routes[i].method < routes[j].method
	})

	b.WriteString("## " + title + "\n\n")
	b.WriteString("| Method | Path | Scope |\n|---|---|---|\n")
	for _, r := range routes {
		b.WriteString("| `" + r.method + "` | `" + r.path + "` | `" + r.scope + "` |\n")
	}
	b.WriteString("\n")
}
