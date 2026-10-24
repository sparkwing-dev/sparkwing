package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

var supportedPlatforms = []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64"}

// Include subpackages, but keep similarly named wire and value packages separate.
var sdkDeniedPackages = []string{
	"internal/admission", "internal/artifacts", "internal/backend",
	"internal/cluster", "internal/controller", "internal/credentials",
	"internal/crons", "internal/deployment", "internal/engine",
	"internal/githubapp", "internal/githubauth", "internal/githuboidc",
	"internal/googleauth", "internal/localsecrets", "internal/mailer",
	"internal/nodehost", "internal/oidcissuer", "internal/orchestrator",
	"internal/pipelineaccept", "internal/pipelineentry", "internal/repos",
	"internal/runners", "internal/secretsource", "internal/source",
	"internal/sparks", "internal/storage", "internal/store", "internal/web",
	"internal/wingd",
}

func sdkDependencyFindings(repo string) ([]string, error) {
	var problems []string
	for _, target := range supportedPlatforms {
		goos, goarch, _ := strings.Cut(target, "/")
		cmd := procenv.ControlCommand(append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch), "go", "list", "-deps", "-f", "{{.ImportPath}}", "./sparkwing/...")
		cmd.Dir = repo
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("SDK production dependencies (%s): %w: %s", target, err, strings.TrimSpace(stderr.String()))
		}
		for dependency := range strings.FieldsSeq(string(out)) {
			for _, denied := range sdkDeniedPackages {
				prefix := modulePath + "/" + denied
				if dependency == prefix || strings.HasPrefix(dependency, prefix+"/") {
					problems = append(problems, "SDK production dependency closure ("+target+") reaches server package "+dependency)
				}
			}
		}
	}
	return dedupe(problems), nil
}
