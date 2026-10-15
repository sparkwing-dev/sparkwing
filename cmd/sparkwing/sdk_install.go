package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type sdkInstaller struct {
	version     string
	environment []string
	readPin     func(string, engine.SDKKind) (engine.Pin, error)
	versionFor  func(engine.Pin) (string, error)
	prepare     func(context.Context, string, engine.SDKKind, []string, io.Writer) (engine.SDKCommand, error)
}

func runSDKInstall(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	installer := sdkInstaller{version: installedVersion(), environment: os.Environ(), readPin: engine.ReadPin, versionFor: engine.VersionForPin, prepare: prepareInstalledSDK}
	return installer.run(ctx, args, os.Stdin, os.Stdout, os.Stderr)
}

func (s sdkInstaller) run(ctx context.Context, args []string, input io.Reader, out, diagnostics io.Writer) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"__sdk-install"}, args...))
	if err != nil {
		return err
	}
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), out, inv.Output(false))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var request engine.SDKInstallRequest
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return errors.New("SDK installation requires a directory, pipeline_dir and SDK JSON request")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("SDK installation requires exactly one JSON request")
	}
	dir, err := sdkInstallDirectory(request)
	if err != nil {
		return err
	}
	pin, err := s.readPin(request.Directory, request.SDK)
	if err != nil {
		return err
	}
	if pin.Manifest == "" || filepath.IsAbs(pin.Manifest) || filepath.Dir(filepath.Join(request.Directory, pin.Manifest)) != dir {
		return errors.New("SDK pipeline directory does not contain the selected pin manifest")
	}
	version, err := s.versionFor(pin)
	if err != nil {
		return err
	}
	if pin.SDK != request.SDK || version != s.version {
		return errors.New("SDK installation requires the engine matching the checkout's exact SDK pin")
	}
	masker := secrets.NewMasker()
	env, err := procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: s.environment, Masker: masker}, procenv.Runtime{})
	if err != nil {
		return err
	}
	masked := &installDiagnostics{out: diagnostics, stream: masker.Stream()}
	result, prepareErr := s.prepare(ctx, dir, request.SDK, env, masked)
	if err := errors.Join(prepareErr, masked.close(), ctx.Err()); err != nil {
		return &maskedCLIError{err: err, masker: masker}
	}
	current, err := s.readPin(request.Directory, request.SDK)
	if err != nil {
		return err
	}
	if current != pin {
		return errors.New("SDK pin changed during installation")
	}
	if result.Directory != dir || len(result.Command) == 0 || !filepath.IsAbs(result.Command[0]) {
		return errors.New("SDK installation returned an invalid command")
	}
	return json.NewEncoder(out).Encode(result)
}

func sdkInstallDirectory(request engine.SDKInstallRequest) (string, error) {
	if !filepath.IsAbs(request.Directory) || request.PipelineDir == "" || filepath.IsAbs(request.PipelineDir) {
		return "", errors.New("SDK installation requires an absolute checkout and a relative pipeline directory")
	}
	dir := filepath.Join(request.Directory, request.PipelineDir)
	rel, err := filepath.Rel(request.Directory, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("SDK pipeline directory must be inside its checkout")
	}
	if request.SDK != engine.GoSDK && request.SDK != engine.TypeScriptSDK {
		return "", errors.New("SDK installation requires go or typescript")
	}
	return dir, nil
}

type installDiagnostics struct {
	mu     sync.Mutex
	out    io.Writer
	stream *secrets.Stream
	err    error
}

func (w *installDiagnostics) Write(body []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil {
		w.err = w.write(w.stream.Mask(string(body), false))
	}
	if w.err != nil {
		return 0, w.err
	}
	return len(body), nil
}

func (w *installDiagnostics) write(text string) error {
	n, err := io.WriteString(w.out, text)
	if err == nil && n != len(text) {
		err = io.ErrShortWrite
	}
	return err
}

func (w *installDiagnostics) close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil {
		w.err = w.write(w.stream.Mask("", true))
	}
	return w.err
}

func prepareInstalledSDK(ctx context.Context, dir string, sdk engine.SDKKind, env []string, diagnostics io.Writer) (engine.SDKCommand, error) {
	result := engine.SDKCommand{Directory: dir}
	if sdk == engine.TypeScriptSDK {
		if err := pipelineentry.InstallTypeScript(ctx, dir, env, diagnostics); err != nil {
			return result, err
		}
		var pkg struct {
			Main string `json:"main"`
		}
		body, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil {
			return result, err
		}
		if err := json.Unmarshal(body, &pkg); err != nil || pkg.Main == "" {
			return result, errors.New("TypeScript package.json requires a main pipeline entry")
		}
		main := filepath.Join(dir, filepath.FromSlash(pkg.Main))
		if _, err := os.Stat(main); err != nil {
			return result, err
		}
		node, err := exec.LookPath("node")
		if err != nil {
			return result, err
		}
		node, err = filepath.Abs(node)
		result.Command = []string{node, main}
		return result, err
	}
	if sdk != engine.GoSDK {
		return result, errors.New("SDK installation requires go or typescript")
	}
	key, parts, err := bincache.ExplainCacheKey(dir, env)
	if err != nil {
		return result, err
	}
	lease, _, err := pipelineBinary(ctx, dir, key, parts, env, diagnostics)
	if err != nil {
		return result, err
	}
	result.Command = []string{lease.Path()}
	return result, lease.Release()
}
