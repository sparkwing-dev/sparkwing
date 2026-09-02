// Command apispec rewrites the OpenAPI document in place from the
// controller's route table. It owns the path and method inventory and the
// `x-sparkwing-scope` value on every operation; summaries, schemas, and
// examples stay hand-written in the same file.
//
// Usage:
//
//	apispec <repo-root> <spec-file>   # rewritten document on stdout
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/apiroutes"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: apispec <repo-root> <spec-file>")
		os.Exit(2)
	}
	root, specPath := os.Args[1], os.Args[2]

	// #nosec G703 -- a build-time tool reading paths the operator names
	data, err := os.ReadFile(specPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apispec:", err)
		os.Exit(1)
	}
	scopes, err := apiroutes.Scopes(filepath.Join(root, "pkg", "controller", "auth.go"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "apispec:", err)
		os.Exit(1)
	}
	routes, err := apiroutes.Parse(filepath.Join(root, "pkg", "controller", "server.go"), scopes)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apispec:", err)
		os.Exit(1)
	}

	out, err := rewrite(string(data), routes, scopeValues(scopes))
	if err != nil {
		fmt.Fprintf(os.Stderr, "apispec: %s: %s\n", specPath, err)
		os.Exit(1)
	}
	fmt.Print(out)
}
