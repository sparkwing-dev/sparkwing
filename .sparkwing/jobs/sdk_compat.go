package jobs

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func runSDKCompatibilityWarm(ctx context.Context) error {
	return runSDKCompatibility(ctx, "warm")
}

func runSDKCompatibilityCold(ctx context.Context) error {
	return runSDKCompatibility(ctx, "cold")
}

func runSDKCompatibility(ctx context.Context, mode string) error {
	return withProductTestHome(func(home string) error {
		command := boundedGoCommand(currentHost(), "test", "-v -count=1 -timeout 15m ./internal/nodehost -run '^TestHistoricalSDKCompatibility$' -args -sdk-compat="+mode)
		if _, err := sparkwing.Bash(ctx, productTestScript(command, home)).Env("GOWORK", "off").Run(); err != nil {
			return fmt.Errorf("historical SDK compatibility (%s): %w", mode, err)
		}
		return nil
	})
}

func skipSDKCompatibilityUnlessTouched(ctx context.Context) bool {
	files, _, err := affectedChangeScope(ctx)
	if err != nil {
		sparkwing.Info(ctx, "sdk-compat: running, because the change could not be judged: %v", err)
		return false
	}
	if why := sdkCompatibilityChange(files); why != "" {
		sparkwing.Info(ctx, "sdk-compat: running, because %s changed", why)
		return false
	}
	sparkwing.Info(ctx, "sdk-compat: skipping, because the change touches no host, SDK, protocol, environment, process, secret or pin code")
	return true
}

func sdkCompatibilityChange(files []string) string {
	for _, file := range files {
		if strings.HasSuffix(file, ".md") {
			continue
		}
		switch file {
		case "go.mod", "go.sum", ".sparkwing/go.mod", ".sparkwing/go.sum",
			".sparkwing/jobs/sdk_compat.go", ".sparkwing/jobs/sdk_compat_test.go",
			".sparkwing/jobs/gate.go", ".sparkwing/jobs/pre_release.go":
			return file
		}
		for _, prefix := range []string{
			"sparkwing/", "sdk/typescript/", "internal/nodehost/", "internal/nodeproto/", "internal/sdkserve/",
			"internal/engine/", "internal/sdkref/", "internal/engineid/", "internal/pipelineentry/",
			"internal/pipelineaccept/", "internal/procenv/", "internal/procgroup/", "internal/protofd/",
			"internal/sessionledger/", "internal/execdiag/", "internal/envredact/", "internal/secrets/",
			"internal/localsecrets/", "internal/logutil/", "internal/logpretty/", "internal/runners/",
			"internal/orchestrator/", "internal/sdkcompat/", "testdata/sdk-compat/",
			"pkg/scaffold/", "pkg/secretname/", "pkg/wingwire/",
		} {
			if strings.HasPrefix(file, prefix) {
				return file
			}
		}
		if path.Base(file) == "go.mod" || path.Base(file) == "go.sum" {
			return file
		}
	}
	return ""
}
