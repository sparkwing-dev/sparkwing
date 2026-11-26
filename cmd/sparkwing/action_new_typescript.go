package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

const typeScriptSDKVersion = "0.75.0"

// safety: `pipeline upgrade` type-checks with the project's own tsc, so the scaffold locks
// the compiler beside the SDK rather than relying on whatever tsc is on PATH.
const typeScriptCompilerVersion = "5.9.3"

const typeScriptNodeTypesVersion = "22.20.5"

// safety: Node runs the pipeline by stripping types, so the config only type-checks: erasable
// syntax, explicit .ts imports, and NodeNext resolution of the SDK package.
const typeScriptConfig = `{
  "compilerOptions": {
    "target": "ES2023",
    "module": "NodeNext",
    "moduleResolution": "NodeNext",
    "strict": true,
    "noEmit": true,
    "allowImportingTsExtensions": true,
    "erasableSyntaxOnly": true,
    "verbatimModuleSyntax": true,
    "skipLibCheck": true,
    "types": ["node"]
  },
  "include": ["**/*.ts"]
}
`

const typeScriptDocsPointer = "// TypeScript API: `sparkwing docs read --topic sdk` pairs each Go example with its\n" +
	"// TypeScript form, and `sparkwing docs read --topic sdk-typescript-reference` lists every export.\n"

func scaffoldTypeScriptPipeline(cwd string, inv clisurface.Invocation, mode string) error {
	name := inv.Value("pipeline")
	trigger, err := resolveTrigger(builtinShape{}, parseOnFlag(inv.Flags["on"]), inv.Has("on"))
	if err != nil {
		return err
	}
	dir, found := walkUpForSparkwing(cwd)
	if !found {
		dir = filepath.Join(cwd, ".sparkwing")
	}
	if cfg, err := projectconfig.Load(filepath.Join(dir, projectconfig.Filename)); err == nil && cfg != nil {
		for _, pipeline := range cfg.Pipelines {
			if pipeline.Name == name {
				return fmt.Errorf("pipeline %q already exists in sparkwing.yaml", name)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		return errors.New("pipeline new: this project uses the Go SDK; select a Go template or create the TypeScript pipeline in another checkout")
	}
	if err := os.MkdirAll(filepath.Join(dir, "jobs"), 0o755); err != nil {
		return err
	}
	configPath := filepath.Join(dir, projectconfig.Filename)
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(configPath, []byte("pipelines: []\n"), 0o644); err != nil {
			return err
		}
	}
	manifestPath := filepath.Join(dir, "package.json")
	manifest, err := os.ReadFile(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		manifest = []byte(fmt.Sprintf("{\n  \"private\": true,\n  \"type\": \"module\",\n  \"main\": \"index.ts\",\n  \"dependencies\": {\"@sparkwing/sdk\": %s},\n  \"devDependencies\": {\"@types/node\": %s, \"typescript\": %s}\n}\n", strconv.Quote(typeScriptSDKVersion), strconv.Quote(typeScriptNodeTypesVersion), strconv.Quote(typeScriptCompilerVersion)))
		if err := os.WriteFile(manifestPath, manifest, 0o644); err != nil {
			return err
		}
		if err := writeTypeScriptConfig(dir); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	var pkg struct {
		Main string `json:"main"`
	}
	if err := json.Unmarshal(manifest, &pkg); err != nil {
		return err
	}
	if pkg.Main == "" {
		return errors.New("pipeline new: package.json needs a main TypeScript entry, such as index.ts")
	}
	entry := filepath.Join(dir, filepath.FromSlash(pkg.Main))
	body, err := os.ReadFile(entry)
	if errors.Is(err, os.ErrNotExist) {
		body = []byte("import { main } from \"@sparkwing/sdk\";\n\nawait main();\n")
	} else if err != nil {
		return err
	}
	file := filepath.Join(dir, "jobs", name+".ts")
	// #nosec G703 -- the pipeline name is validated as kebab-case before scaffolding in the selected project
	if _, err := os.Stat(file); err == nil {
		return fmt.Errorf("refusing to overwrite %s; pick a different --pipeline", file)
	}
	short := inv.Value("short")
	if short == "" {
		short = "Run " + name
	}
	source := fmt.Sprintf("import { definePipeline } from \"@sparkwing/sdk\";\n\ndefinePipeline({\n  name: %s,\n  short: %s,\n  plan(p) {\n    p.job(%s, async (ctx) => { ctx.log.info(%s); });\n  },\n});\n", strconv.Quote(kebabToPascal(name)), strconv.Quote(short), strconv.Quote(name), strconv.Quote(short))
	if inv.Value("template") == "merge-queue-typescript" {
		source = renderBuiltinTemplate(name, short, mergeQueueTypeScriptTemplate)
	}
	source = typeScriptDocsPointer + source
	// #nosec G703 -- the pipeline name is validated as kebab-case before scaffolding in the selected project
	if err := os.WriteFile(file, []byte(source), 0o644); err != nil {
		return err
	}
	if err := appendPipelinesYAML(dir, name, kebabToPascal(name), inv.Bool("hidden"), trigger); err != nil {
		// #nosec G703 -- rollback removes the validated pipeline filename this scaffold just wrote
		return errors.Join(err, os.Remove(file))
	}
	relative, err := filepath.Rel(filepath.Dir(entry), file)
	if err != nil {
		return err
	}
	body = append(body, []byte("\nimport "+strconv.Quote("./"+filepath.ToSlash(relative))+";\n")...)
	// #nosec G703 -- scaffolding updates the main entry declared by the operator-selected local package.json
	if err := os.WriteFile(entry, body, 0o644); err != nil {
		return err
	}
	return finishScaffold(dir, file, name, !found, trigger, mode)
}

func writeTypeScriptConfig(dir string) error {
	file, err := os.OpenFile(filepath.Join(dir, "tsconfig.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = file.WriteString(typeScriptConfig)
	return errors.Join(err, file.Close())
}

// safety: the run installs the SDK with npm ci, which refuses a project without package-lock.json.
func lockTypeScriptSkeleton(ctx context.Context, sparkwingDir string) error {
	args := engine.TypeScriptLockfileArgs()
	rerun := "npm --prefix " + shellSingleQuote(sparkwingDir) + " " + strings.Join(args, " ")
	masker := secrets.NewMasker()
	environment, err := procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: os.Environ(), Masker: masker}, procenv.Runtime{})
	if err != nil {
		return err
	}
	cmd, err := pipelineentry.NPMCommand(ctx, environment, runtime.GOOS, exec.LookPath, args...)
	if err != nil {
		return fmt.Errorf("pipeline new: the pipeline files are written, but npm is not on PATH to create package-lock.json; install Node.js 22.18 or later with npm, then run:\n  %s", rerun)
	}
	cmd.Dir = sparkwingDir
	var captured bytes.Buffer
	cmd.Stdout = &captured
	cmd.Stderr = &captured
	stop := startSpinner("resolving dependencies (`npm install --package-lock-only`)")
	err = cmd.Run()
	stop()
	if err != nil {
		return fmt.Errorf("pipeline new: the pipeline files are written, but creating package-lock.json failed: %w\n%s\nfix the error, then run:\n  %s", err, strings.TrimSpace(masker.Mask(captured.String())), rerun)
	}
	return nil
}
