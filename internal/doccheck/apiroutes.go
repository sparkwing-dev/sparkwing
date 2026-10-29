package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

type apiRouteReference struct {
	method, path string
	line         int
}

var (
	apiRouteExample   = regexp.MustCompile("\\b(GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS|TRACE)\\s+(/(?:api/v1|node/v1)(?:/[^\\s`\"'<>)]*)?)")
	apiRouteParameter = regexp.MustCompile(`\{[^{}]+\}`)
)

func documentedAPIRoutes(doc string) []apiRouteReference {
	var result []apiRouteReference
	for _, match := range apiRouteExample.FindAllStringSubmatchIndex(doc, -1) {
		path, _, _ := strings.Cut(doc[match[4]:match[5]], "?")
		result = append(result, apiRouteReference{
			method: doc[match[2]:match[3]], path: strings.TrimRight(path, ".,;:"),
			line: strings.Count(doc[:match[0]], "\n") + 1,
		})
	}
	return result
}

func knownAPIRoute(method, path string) bool {
	path = apiRouteParameter.ReplaceAllString(path, "{}")
	for _, route := range capability.Routes() {
		if route.Method == method && apiRouteParameter.ReplaceAllString(route.Path, "{}") == path {
			return true
		}
	}
	return false
}

func checkAPIRoutes(contentDir string) bool {
	var checked, failed int
	// #nosec G703 -- a build-time tool reading the documentation directory the operator selects
	err := filepath.WalkDir(contentDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "migrations" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, ref := range documentedAPIRoutes(string(body)) {
			checked++
			if !knownAPIRoute(ref.method, ref.path) {
				failed++
				rel, _ := filepath.Rel(contentDir, path)
				fmt.Printf("doccheck/api-routes: %s:%d: %s %s is outside the shared policy\n", rel, ref.line, ref.method, ref.path)
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "doccheck/api-routes:", err)
		return false
	}
	fmt.Printf("doccheck/api-routes: %d route reference(s), %d unresolved\n", checked, failed)
	return failed == 0
}
