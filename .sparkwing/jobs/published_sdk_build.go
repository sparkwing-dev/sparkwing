package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func runPublishedSDKBuild(ctx context.Context) error {
	return buildPublishedSDK(ctx, goStepParallelism(currentHost()))
}

// safety: workspace and alternate modfiles can compile unpublished SDK APIs.
// Neither is evidence that the release's pinned module can build these commands.
func buildPublishedSDK(ctx context.Context, cores int) error {
	const environment = "GOWORK=off GOENV=off GOFLAGS= CGO_ENABLED=0 "
	root := regexCheckRoot()
	out, err := sparkwing.Bash(ctx, environment+goCommandAt(cores, "list", "-mod=readonly -m -json "+sdkModulePath)).Dir(root).String()
	if err != nil {
		return fmt.Errorf("published SDK: resolve the root module's pin: %w", err)
	}
	var module struct {
		Path    string
		Version string
		Replace *json.RawMessage
	}
	if err := json.Unmarshal([]byte(out), &module); err != nil {
		return fmt.Errorf("published SDK: decode root module's pin: %w", err)
	}
	if module.Path != sdkModulePath || module.Version == "" || module.Replace != nil {
		return fmt.Errorf("published SDK: root must require %s at a published version without replace; publish the SDK before updating the root pin", sdkModulePath)
	}
	sparkwing.Info(ctx, "published SDK: building release commands against %s %s without workspace or replace", module.Path, module.Version)
	command := environment + goCommandAt(cores, "build", "-mod=readonly -trimpath -o "+shellQuote(os.DevNull)+" ./cmd/sparkwing ./cmd/sparkwing-controller ./cmd/sparkwing-runner")
	if _, err := sparkwing.Bash(ctx, command).Dir(root).Run(); err != nil {
		return fmt.Errorf("published SDK release build: publish SDK API changes and update the root pin before using them: %w", err)
	}
	return nil
}

func runPublishedSDKBuildTouched(ctx context.Context) error {
	files, _, err := pushRangeScope(ctx, "file(s)", func(files []string) []string { return files })
	if err != nil {
		return err
	}
	return runPublishedSDKBuildFiles(ctx, files, prePushTaskCores(currentHost().cpus))
}

func runPublishedSDKBuildFiles(ctx context.Context, files []string, cores int) error {
	why := goChange(files, nil)
	var err error
	if why == "" {
		why, err = repoGoFilesChange(ctx, files, "")
	}
	if err != nil {
		return err
	}
	if why == "" {
		sparkwing.Info(ctx, "published SDK: no Go inputs changed; skipping release build")
		return nil
	}
	return buildPublishedSDK(ctx, cores)
}
