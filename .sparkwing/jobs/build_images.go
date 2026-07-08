package jobs

import (
	"context"
	"fmt"
	"os"
	"strings"

	sparkwing "github.com/sparkwing-dev/sparkwing/sparkwing"
)

// BuildImages builds one Docker image per deployable sparkwing
// component using build/Dockerfile.binary. The Dockerfile is
// parameterized by a BINARY arg, so one source pass produces five
// images.
//
// Modes:
//   - local (default): single-arch image into the local docker daemon,
//     tagged <component>:<tag>. Useful for offline dev, smoke tests.
//   - registry+push: multi-arch buildx push to the supplied registry,
//     producing <registry>/<component>:<tag>. The caller is
//     responsible for being logged in (e.g. `aws ecr get-login-password
//     ... | docker login ...`).
//
// Cross-process consumers (kikd-infra/release-sparkwing) read the
// final "RELEASE_IMAGES" summary line written to stdout.
type BuildImages struct {
	sparkwing.Base

	args BuildImagesArgs

	// Populated by resolve-tag from --tag or `git rev-parse --short HEAD`.
	tag string

	// fully-qualified refs computed once at resolve-tag and reused by
	// downstream steps' summary line.
	refs []string
}

type BuildImagesArgs struct {
	Registry string `flag:"registry" desc:"Optional registry prefix (e.g. an ECR host). When set, images are tagged <registry>/<component>:<tag>."`
	Push     bool   `flag:"push" desc:"Build multi-arch (linux/amd64,linux/arm64) and push to the registry. Requires --registry."`
	Tag      string `flag:"tag" desc:"Override the image tag. Default: commit-<short-sha> resolved from the current HEAD."`
	SkipWeb  bool   `flag:"skip-web-bundle" desc:"Skip the Next.js SPA build. Use when internal/web/next-out/ is already current (faster iteration)."`
}

// buildImageSpec is the per-component build recipe.
type buildImageSpec struct {
	// name matches both a cmd/<name>/ source dir and the image name
	// pushed to the registry.
	name string
	// dockerfile is the path under the repo root; empty means
	// build/Dockerfile.binary (the standard single-binary recipe).
	dockerfile string
}

// components lists the binaries that get an image. The standard
// components share build/Dockerfile.binary; sparkwing-runner needs
// extra runtime tooling (git, a netrc-seeding entrypoint wrapper)
// so it has its own dockerfile.
var buildImagesComponents = []buildImageSpec{
	{name: "sparkwing-controller"},
	{name: "sparkwing-web"},
	{name: "sparkwing-logs"},
	{name: "sparkwing-cache"},
	{name: "sparkwing-runner", dockerfile: "build/Dockerfile.runner"},
}

func (BuildImages) ShortHelp() string {
	return "Build one Docker image per deployable sparkwing component"
}

func (BuildImages) Help() string {
	return "Builds sparkwing-controller, sparkwing-web, sparkwing-logs, sparkwing-cache, and sparkwing-runner images from build/Dockerfile.binary. By default produces single-arch images in the local daemon. With --registry --push, builds multi-arch (amd64 + arm64) and pushes directly to the configured registry; the caller must be logged in. Prints a final RELEASE_IMAGES line that cross-process consumers parse for the image refs."
}

func (BuildImages) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Local single-arch build, tagged commit-<sha>", Command: "sparkwing run build-images"},
		{Comment: "Push multi-arch to a registry", Command: "sparkwing run build-images --registry=633280902600.dkr.ecr.us-west-2.amazonaws.com --push"},
		{Comment: "Reuse a current SPA bundle to skip the slow npm step", Command: "sparkwing run build-images --skip-web-bundle"},
	}
}

func (p *BuildImages) Plan(_ context.Context, plan *sparkwing.Plan, in BuildImagesArgs, _ sparkwing.RunContext) error {
	if in.Push && in.Registry == "" {
		return fmt.Errorf("build-images: --push requires --registry")
	}
	p.args = in
	sparkwing.Job(plan, "build", p).Inline()
	return nil
}

func (p *BuildImages) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	resolveTag := sparkwing.Step(w, "resolve-tag", p.resolveTag)
	builder := sparkwing.Step(w, "ensure-buildx-builder", p.ensureBuildxBuilder).Needs(resolveTag)
	webBundle := sparkwing.Step(w, "build-web-bundle", p.buildWebBundle).Needs(builder)

	prev := webBundle
	for _, comp := range buildImagesComponents {
		c := comp
		prev = sparkwing.Step(w, "build-"+c.name, func(ctx context.Context) error {
			return p.buildOne(ctx, c)
		}).Needs(prev)
	}

	sparkwing.Step(w, "summary", p.summary).Needs(prev)
	return nil, nil
}

// ensureBuildxBuilder makes sure a docker-container buildx builder is
// available and selected. Multi-arch buildx builds (--platform=linux/
// amd64,linux/arm64) need the docker-container driver; the default
// builder shipped with docker uses the docker driver and silently
// degrades to single-arch on multi-platform builds. Creating + booting
// the builder up front means downstream build-<component> steps don't
// race on first-use bootstrap.
//
// Idempotent: if the builder already exists, the create call's
// "ERROR: existing instance" is treated as success and we just
// select + bootstrap.
const buildxBuilderName = "sparkwing-multiarch"

func (p *BuildImages) ensureBuildxBuilder(ctx context.Context) error {
	existing, err := sparkwing.Exec(ctx, "docker", "buildx", "inspect", buildxBuilderName).Capture()
	if err == nil {
		sparkwing.Info(ctx, "reusing existing buildx builder %q", buildxBuilderName)
		_, _ = existing.Stdout, existing.Stderr
	} else {
		sparkwing.Info(ctx, "creating buildx builder %q (docker-container driver)", buildxBuilderName)
		_, cerr := sparkwing.Exec(ctx, "docker", "buildx", "create",
			"--name", buildxBuilderName,
			"--driver", "docker-container",
			"--bootstrap",
		).Run()
		if cerr != nil {
			return fmt.Errorf("create buildx builder: %w", cerr)
		}
	}
	if _, err := sparkwing.Exec(ctx, "docker", "buildx", "use", buildxBuilderName).Run(); err != nil {
		return fmt.Errorf("select buildx builder: %w", err)
	}
	return nil
}

func (p *BuildImages) resolveTag(ctx context.Context) error {
	if p.args.Tag != "" {
		p.tag = p.args.Tag
	} else {
		sha, err := sparkwing.Exec(ctx, "git", "rev-parse", "--short", "HEAD").String()
		if err != nil {
			return fmt.Errorf("resolve git sha: %w", err)
		}
		p.tag = "commit-" + sha
	}

	prefix := ""
	if p.args.Registry != "" {
		prefix = strings.TrimSuffix(p.args.Registry, "/") + "/"
	}
	p.refs = make([]string, 0, len(buildImagesComponents))
	for _, c := range buildImagesComponents {
		p.refs = append(p.refs, prefix+c.name+":"+p.tag)
	}

	sparkwing.Info(ctx, "resolved tag=%s; %d images to build", p.tag, len(p.refs))
	sparkwing.Annotate(ctx, "tag: "+p.tag)
	return nil
}

func (p *BuildImages) buildWebBundle(ctx context.Context) error {
	if p.args.SkipWeb {
		sparkwing.Info(ctx, "skipping SPA build per --skip-web-bundle")
		return nil
	}
	sparkwing.Info(ctx, "running bin/build-web.sh (npm ci + next build)")
	_, err := sparkwing.Exec(ctx, "bash", sparkwing.Path("bin/build-web.sh")).Run()
	return err
}

func (p *BuildImages) buildOne(ctx context.Context, spec buildImageSpec) error {
	imageRef := p.refForComponent(spec.name)
	dockerfile := spec.dockerfile
	if dockerfile == "" {
		dockerfile = "build/Dockerfile.binary"
	}

	args := []string{
		"buildx", "build",
		"--file", sparkwing.Path(dockerfile),
		"--build-arg", "BINARY=" + spec.name,
		"--build-arg", "SPARKWING_VERSION=" + p.tag,
		"--tag", imageRef,
	}
	if p.args.Push {
		args = append(args, "--platform", "linux/amd64,linux/arm64", "--push")
	} else {
		args = append(args, "--load")
	}
	args = append(args, sparkwing.Path("."))

	sparkwing.Info(ctx, "docker buildx build %s -> %s (%s)", spec.name, imageRef, dockerfile)
	_, err := sparkwing.Exec(ctx, "docker", args...).Run()
	if err != nil {
		return fmt.Errorf("build %s: %w", spec.name, err)
	}
	sparkwing.Annotate(ctx, spec.name+" -> "+imageRef)
	return nil
}

func (p *BuildImages) refForComponent(name string) string {
	for i, c := range buildImagesComponents {
		if c.name == name {
			return p.refs[i]
		}
	}
	return name + ":" + p.tag
}

// summary emits a single parseable line to stdout that cross-process
// callers grep for. The format is intentionally simple:
//
//	RELEASE_IMAGES tag=<tag> images=<ref>,<ref>,<ref>...
//
// Any consumer can split on the comma to get the per-component refs.
func (p *BuildImages) summary(ctx context.Context) error {
	line := fmt.Sprintf("RELEASE_IMAGES tag=%s images=%s",
		p.tag, strings.Join(p.refs, ","))
	sparkwing.Info(ctx, "%s", line)
	_, _ = fmt.Fprintln(os.Stdout, line)
	sparkwing.Summary(ctx, "## Built images\n\n- tag: `"+p.tag+"`\n- refs:\n  - `"+strings.Join(p.refs, "`\n  - `")+"`\n")
	return nil
}

func init() {
	sparkwing.Register[BuildImagesArgs]("build-images", func() sparkwing.Pipeline[BuildImagesArgs] { return &BuildImages{} })
}
