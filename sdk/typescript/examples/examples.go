// Package tsexamples embeds the TypeScript example pipelines that
// `sparkwing examples` lists beside the Go templates.
package tsexamples

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
)

//go:embed lint-test-node-ts secret-and-cache-ts test-matrix-artifacts-deploy-ts
var files embed.FS

// Example is one TypeScript example: its catalog metadata, README and source.
type Example struct {
	Name         string
	Category     string
	Description  string
	WhenToUse    string
	Prerequisite string
	ReadMe       string
	Source       string
}

var catalog = []Example{
	{
		Name:         "lint-test-node-ts",
		Category:     "ci-hygiene",
		Description:  "TypeScript CI check for a Node project: install once, then lint and test as parallel steps of one job with a cached npm install.",
		WhenToUse:    "Start here for a TypeScript pipeline that gates a Node project on lint and tests.",
		Prerequisite: "A package.json with lint and test scripts, and its package-lock.json.",
	},
	{
		Name:         "test-matrix-artifacts-deploy-ts",
		Category:     "deploy",
		Description:  "TypeScript port of a typical GitHub Actions workflow: a Node version test matrix with jobFanOut, a build that publishes dist/ as an artifact, and a deploy that consumes it and runs only from main.",
		WhenToUse:    "Port a GitHub Actions matrix, upload-artifact and deploy-on-main workflow to a TypeScript pipeline.",
		Prerequisite: "A package.json with a build script that writes dist/, tests node --test can run, and an executable scripts/deploy.sh.",
	},
	{
		Name:         "secret-and-cache-ts",
		Category:     "deploy",
		Description:  "TypeScript pipeline that tests with a cached npm install, then deploys with a declared, masked secret, a retry and timeouts.",
		WhenToUse:    "Read a declared secret from a TypeScript job, and see the preflight that refuses a run whose secret is unset.",
		Prerequisite: "A package.json with a test script, an executable scripts/deploy.sh, and DEPLOY_TOKEN stored with sparkwing secrets set.",
	},
}

// List returns every TypeScript example, each with its README and source.
func List() ([]Example, error) {
	out := make([]Example, 0, len(catalog))
	for _, e := range catalog {
		readme, err := fs.ReadFile(files, path.Join(e.Name, "README.md"))
		if err != nil {
			return nil, fmt.Errorf("read README for %s: %w", e.Name, err)
		}
		source, err := fs.ReadFile(files, path.Join(e.Name, "pipeline.ts"))
		if err != nil {
			return nil, fmt.Errorf("read source for %s: %w", e.Name, err)
		}
		e.ReadMe, e.Source = string(readme), string(source)
		out = append(out, e)
	}
	return out, nil
}
