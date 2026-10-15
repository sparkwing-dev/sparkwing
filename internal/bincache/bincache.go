package bincache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

var ErrMiss = errors.New("remote binary cache: miss")

func mkdirCache(dir string) error {
	err := os.MkdirAll(dir, 0o755)
	if err == nil {
		return nil
	}
	if !underSparkwingHome(dir) {
		return err
	}
	return fmt.Errorf("%w\n  sparkwing holds this cache under its home directory."+
		" Set SPARKWING_HOME to a writable directory to place it elsewhere"+
		" (default: ~/.sparkwing)", err)
}

// safety: callers hand bincache a destination it does not choose, and some of those
// sit in the consumer's own checkout, where SPARKWING_HOME moves nothing.
func underSparkwingHome(dir string) bool {
	home, err := paths.DefaultPaths()
	if err != nil || home.Root == "" {
		return false
	}
	rel, err := filepath.Rel(home.Root, dir)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func SparkwingHome() string {
	p, err := paths.DefaultPaths()
	if err != nil {
		return ".sparkwing"
	}
	return p.Root
}

var ErrMissingGoSum = errors.New("missing go.sum entries")

type CompileError struct {
	Output []byte
	Err    error
}

func (e *CompileError) Error() string { return fmt.Sprintf("compile .sparkwing/: %v", e.Err) }
func (e *CompileError) Unwrap() error { return e.Err }

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (lb *lockedBuffer) Write(p []byte) (int, error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.buf.Write(p)
}

func (lb *lockedBuffer) String() string {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.buf.String()
}

func (lb *lockedBuffer) Bytes() []byte {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return append([]byte(nil), lb.buf.Bytes()...)
}

// pipelineBuildFlags are the `go build` flags CompilePipeline passes. keyParts
// hashes them into the cache key: the cache serves a stored binary without ever
// digesting its bytes, so a flag the key does not name would hand every local
// and shared cache a stale binary forever. Tests replace it to prove that.
var pipelineBuildFlags = []string{"-trimpath", "-ldflags", "-s -w"}

// CompileOptions binds compiler subprocesses and diagnostics to captured inputs.
type CompileOptions struct {
	Environment []string
	Diagnostics io.Writer
}

func CompilePipeline(ctx context.Context, sparkwingDir, dest string, options CompileOptions) error {
	return compilePipeline(ctx, sparkwingDir, dest, pipelineBuildFlags, options)
}

// CompileUncachedPipeline preserves debug symbols for execution without the binary cache.
func CompileUncachedPipeline(ctx context.Context, sparkwingDir, dest string, options CompileOptions) error {
	return compilePipeline(ctx, sparkwingDir, dest, nil, options)
}

func compilePipeline(ctx context.Context, sparkwingDir, dest string, flags []string, options CompileOptions) error {
	if options.Environment == nil || options.Diagnostics == nil {
		return errors.New("pipeline compilation requires a captured environment and diagnostic writer")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tool, err := gotoolchain.Executable(options.Environment)
	if err != nil {
		return fmt.Errorf(
			"go toolchain not on PATH: sparkwing compiles .sparkwing/ via `go build`.\n" +
				"  Install Go 1.26+ from https://go.dev/dl/ and re-run",
		)
	}
	if err := mkdirCache(filepath.Dir(dest)); err != nil {
		return err
	}

	args := append([]string{"build"}, flags...)
	var noticeErr error
	ctx = gotoolchain.WithSession(ctx, options.Environment, func(message string) {
		_, err := fmt.Fprintln(options.Diagnostics, message)
		noticeErr = errors.Join(noticeErr, err)
	})
	env, err := gotoolchain.BuildEnv(ctx, sparkwingDir, options.Environment, EffectiveOverlay(sparkwingDir, options.Environment))
	if err := errors.Join(err, noticeErr); err != nil {
		return err
	}
	overlay := overlayModfilePath(sparkwingDir)
	work, workPresent := goWorkInScope(sparkwingDir, env)
	switch {
	case workPresent && !goWorkCovers(work, sparkwingDir):
		if _, err := fmt.Fprintf(options.Diagnostics,
			"warning: %s does not include this .sparkwing module; ignoring it. "+
				"A sparkwing project builds as a self-contained module, not nested "+
				"inside another Go workspace; add it to the go.work `use` list to "+
				"link it deliberately.\n",
			work,
		); err != nil {
			return err
		}
		env = withGoworkOff(env)
		if overlay != "" {
			args = append(args, "-modfile="+overlay)
		}
	case workPresent:
		if overlay != "" {
			if _, err := fmt.Fprintf(options.Diagnostics,
				"warning: %s in effect; skipping sparks resolution. "+
					"Modules resolve from go.mod + workspace, not .resolved.mod. "+
					"To use local copies of sparks libs too, add them to go.work.\n",
				work,
			); err != nil {
				return err
			}
		}
	case overlay != "":
		args = append(args, "-modfile="+overlay)
	}
	args = append(args, "-o", dest, ".")
	cmd := exec.Command(tool, args...)
	cmd.Dir = sparkwingDir
	var captured lockedBuffer
	cmd.Stdout = io.MultiWriter(options.Diagnostics, &captured)
	cmd.Stderr = cmd.Stdout
	cmd.Env = env
	if err := gotoolchain.Run(ctx, cmd); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if strings.Contains(captured.String(), "missing go.sum entry") {
			return ErrMissingGoSum
		}
		if explanation := gotoolchain.ExplainOutput(ctx, captured.String(), env); explanation != nil {
			return &CompileError{Output: []byte(explanation.Error()), Err: explanation}
		}
		return &CompileError{Output: captured.Bytes(), Err: err}
	}
	return nil
}

func overlayModfilePath(sparkwingDir string) string {
	p := filepath.Join(sparkwingDir, ".resolved.mod")
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return p
}

func goWorkInScope(sparkwingDir string, environment []string) (string, bool) {
	value, _ := gotoolchain.LookupEnv(environment, "GOWORK")
	switch env := value; env {
	case "off":
		return "", false
	case "":
	default:
		if fi, err := os.Stat(env); err == nil && fi.Mode().IsRegular() {
			return env, true
		}
		return "", false
	}
	dir := sparkwingDir
	for {
		candidate := filepath.Join(dir, "go.work")
		if fi, err := os.Stat(candidate); err == nil && fi.Mode().IsRegular() {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func goWorkCovers(workPath, moduleDir string) bool {
	raw, err := os.ReadFile(workPath)
	if err != nil {
		return false
	}
	wf, err := modfile.ParseWork(workPath, raw, nil)
	if err != nil {
		return false
	}
	absModule, err := filepath.Abs(moduleDir)
	if err != nil {
		return false
	}
	workDir := filepath.Dir(workPath)
	for _, u := range wf.Use {
		p := u.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(workDir, p)
		}
		ap, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if filepath.Clean(ap) == filepath.Clean(absModule) {
			return true
		}
	}
	return false
}

func withGoworkOff(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if strings.HasPrefix(e, "GOWORK=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, "GOWORK=off")
}

func PipelineCacheKey(sparkwingDir string) (string, error) {
	return PipelineCacheKeyForPlatform(sparkwingDir, runtime.GOOS, runtime.GOARCH)
}

func PipelineCacheKeyForPlatform(sparkwingDir, goos, goarch string) (string, error) {
	parts, err := keyParts(sparkwingDir, goos, goarch, os.Environ())
	if err != nil {
		return "", err
	}
	return foldKey(parts), nil
}

type KeyPart struct {
	Label  string
	Digest string
	Detail string

	material []byte
}

func foldKey(parts []KeyPart) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p.material)
	}
	raw := fmt.Sprintf("%x", h.Sum(nil))
	return raw[:8] + "-" + raw[8:16]
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])[:12]
}

func ExplainCacheKey(sparkwingDir string, env []string) (string, []KeyPart, error) {
	parts, err := keyParts(sparkwingDir, runtime.GOOS, runtime.GOARCH, env)
	if err != nil {
		return "", nil, err
	}
	return foldKey(parts), parts, nil
}

func keyParts(sparkwingDir, goos, goarch string, env []string) ([]KeyPart, error) {
	var parts []KeyPart
	add := func(label, detail string, material []byte) {
		parts = append(parts, KeyPart{
			Label: label, Detail: detail,
			Digest: digestOf(material), material: material,
		})
	}

	add("go toolchain", goMajorMinor(), []byte(fmt.Sprintf("go:%s\n", goMajorMinor())))
	add("platform", goos+"/"+goarch, []byte(fmt.Sprintf("arch:%s/%s\n", goos, goarch)))

	flags := strings.Join(pipelineBuildFlags, " ")
	add("build flags", flags, []byte("buildflags:"+flags+"\n"))
	add("build environment", "captured Go build settings", buildEnvironmentKey(env))

	var moduleBuf bytes.Buffer
	moduleStats, err := hashDirIntoCounted(&moduleBuf, sparkwingDir, allFiles)
	if err != nil {
		return nil, err
	}
	add("module tree", moduleStats.String(), moduleBuf.Bytes())

	goModPath := filepath.Join(sparkwingDir, "go.mod")
	replaceTargets, err := localReplaceTargets(goModPath)
	if err != nil {
		return nil, err
	}
	workTargets, workSummary, err := localWorkspaceTargets(sparkwingDir, env)
	if err != nil {
		return nil, err
	}
	if workSummary != "" {
		add("go.work", "normalized directives", []byte(workSummary))
	}

	replaceTargets = append(replaceTargets, workTargets...)
	sort.Slice(replaceTargets, func(i, j int) bool {
		if replaceTargets[i].Label != replaceTargets[j].Label {
			return replaceTargets[i].Label < replaceTargets[j].Label
		}
		return replaceTargets[i].Dir < replaceTargets[j].Dir
	})
	var last replaceTarget
	for _, t := range replaceTargets {
		if t == last {
			continue
		}
		last = t
		var buf bytes.Buffer

		fmt.Fprintf(&buf, "replace:%s\n", t.Label)
		stats, err := hashDirIntoCounted(&buf, t.Dir, allFiles)
		if err != nil {
			return nil, err
		}
		add("replace "+t.Label, stats.String()+" from "+t.Dir, buf.Bytes())
	}

	for _, overlay := range []struct {
		name   string
		prefix string
	}{
		{".resolved.mod", "resolved-mod:"},
		{".resolved.sum", "resolved-sum:"},
	} {
		p := filepath.Join(sparkwingDir, overlay.name)
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		var buf bytes.Buffer
		fmt.Fprint(&buf, overlay.prefix)
		buf.Write(data)
		fmt.Fprintln(&buf)
		add(overlay.name, "resolved module pins", buf.Bytes())
	}

	return parts, nil
}

type fileFilter func(name string) bool

func allFiles(string) bool { return true }

func goMajorMinor() string {
	v := runtime.Version()
	dots := 0
	for i, c := range v {
		if c == '.' {
			dots++
			if dots == 2 {
				return v[:i]
			}
		}
	}
	return v
}

type HashStats struct {
	Files   int
	Bytes   int64
	Ignored int
}

func (s HashStats) String() string {
	base := fmt.Sprintf("%d files, %s", s.Files, humanSize(s.Bytes))
	if s.Ignored > 0 {
		base += fmt.Sprintf(" (%d gitignored, excluded)", s.Ignored)
	}
	return base
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for cur := n / unit; cur >= unit && exp < 3; cur /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

func hashDirIntoCounted(h io.Writer, dir string, keep fileFilter) (HashStats, error) {
	var stats HashStats
	files, err := walkHashable(dir, keep)
	if err != nil {
		return stats, err
	}
	ignored := ignoredUnder(dir, files)
	for _, path := range files {
		if ignored[path] {
			stats.Ignored++
			continue
		}
		rel, _ := filepath.Rel(dir, path)
		f, err := os.Open(path)
		if err != nil {
			return stats, err
		}
		fileH := sha256.New()
		n, copyErr := io.Copy(fileH, f)
		f.Close()
		if copyErr != nil {
			return stats, copyErr
		}
		stats.Files++
		stats.Bytes += n
		fmt.Fprintf(h, "%s\x00%x\n", rel, fileH.Sum(nil))
	}
	return stats, nil
}

func walkHashable(dir string, keep fileFilter) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", ".claude-scratch":
				return filepath.SkipDir
			}
			return nil
		}
		if !keep(d.Name()) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	return files, err
}

type replaceTarget struct {
	Label string
	Dir   string
}

func replaceLabel(old module.Version) string {
	if old.Version == "" {
		return old.Path
	}
	return old.Path + "@" + old.Version
}

func moduleLabelOf(dir string) (string, error) {
	p := filepath.Join(dir, "go.mod")
	// #nosec G703 -- the go.mod of a workspace module this process resolved
	data, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("workspace module %s: %w", dir, err)
	}
	path := modfile.ModulePath(data)
	if path == "" {
		return "", fmt.Errorf("workspace module %s: no module directive in go.mod", dir)
	}
	return path, nil
}

func localReplaceTargets(goModPath string) ([]replaceTarget, error) {
	data, err := os.ReadFile(goModPath)
	if err != nil {
		return nil, err
	}
	mf, err := modfile.Parse(goModPath, data, nil)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(goModPath)
	var out []replaceTarget
	for _, r := range mf.Replace {
		np := r.New.Path
		if np == "" || !isLocalPath(np) {
			continue
		}
		abs := np
		if !filepath.IsAbs(abs) {
			abs = filepath.Clean(filepath.Join(dir, np))
		}
		out = append(out, replaceTarget{Label: replaceLabel(r.Old), Dir: abs})
	}
	return out, nil
}

func isLocalPath(p string) bool {
	return strings.HasPrefix(p, ".") || strings.HasPrefix(p, "/")
}

func localWorkspaceTargets(sparkwingDir string, env []string) (targets []replaceTarget, summary string, err error) {
	work, ok := goWorkInScope(sparkwingDir, env)
	if !ok || !goWorkCovers(work, sparkwingDir) {
		return nil, "", nil
	}
	raw, err := os.ReadFile(work)
	if err != nil {
		return nil, "", err
	}
	wf, err := modfile.ParseWork(work, raw, nil)
	if err != nil {
		return nil, "", err
	}
	absSparkwing, err := filepath.Abs(sparkwingDir)
	if err != nil {
		return nil, "", err
	}
	absSparkwing = filepath.Clean(absSparkwing)
	workDir := filepath.Dir(work)

	resolve := func(p string) string {
		if p == "" {
			return ""
		}
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(workDir, p)
		}
		abs = filepath.Clean(abs)
		if abs == absSparkwing {
			return ""
		}
		return abs
	}

	var b strings.Builder
	b.WriteString("go.work\n")
	if wf.Go != nil {
		fmt.Fprintf(&b, "go:%s\n", wf.Go.Version)
	}
	if wf.Toolchain != nil {
		fmt.Fprintf(&b, "toolchain:%s\n", wf.Toolchain.Name)
	}
	godebugs := make([]string, 0, len(wf.Godebug))
	for _, g := range wf.Godebug {
		godebugs = append(godebugs, g.Key+"="+g.Value)
	}
	sort.Strings(godebugs)
	for _, g := range godebugs {
		fmt.Fprintf(&b, "godebug:%s\n", g)
	}

	uses := make([]string, 0, len(wf.Use))
	for _, u := range wf.Use {
		abs := resolve(u.Path)
		if abs == "" {
			continue
		}
		label, err := moduleLabelOf(abs)
		if err != nil {
			return nil, "", err
		}
		targets = append(targets, replaceTarget{Label: label, Dir: abs})
		uses = append(uses, label)
	}
	sort.Strings(uses)
	for _, u := range uses {
		fmt.Fprintf(&b, "use:%s\n", u)
	}

	replaces := make([]string, 0, len(wf.Replace))
	for _, r := range wf.Replace {
		label := replaceLabel(r.Old)
		if isLocalPath(r.New.Path) {
			abs := resolve(r.New.Path)
			if abs == "" {
				continue
			}
			targets = append(targets, replaceTarget{Label: label, Dir: abs})

			replaces = append(replaces, label+" => local")
			continue
		}
		replaces = append(replaces, label+" => "+replaceLabel(r.New))
	}
	sort.Strings(replaces)
	for _, r := range replaces {
		fmt.Fprintf(&b, "replace:%s\n", r)
	}

	return targets, b.String(), nil
}

// EffectiveOverlay returns the overlay used when building the pipeline module.
func EffectiveOverlay(dir string, env []string) string {
	work, present := goWorkInScope(dir, env)
	if present && goWorkCovers(work, dir) {
		return ""
	}
	return overlayModfilePath(dir)
}

func buildEnvironmentKey(env []string) []byte {
	var input bytes.Buffer
	for _, name := range strings.Fields("PATH GOOS GOARCH GO386 GOAMD64 GOARM GOARM64 GOMIPS GOMIPS64 GOPPC64 GORISCV64 GOWASM GOEXPERIMENT GOFLAGS GOTOOLCHAIN GOROOT GOPATH GOENV CGO_ENABLED CC CXX FC AR PKG_CONFIG CGO_CFLAGS CGO_CPPFLAGS CGO_CXXFLAGS CGO_FFLAGS CGO_LDFLAGS") {
		value, _ := gotoolchain.LookupEnv(env, name)
		fmt.Fprintf(&input, "%s=%s\x00", name, value)
	}
	sum := sha256.Sum256(input.Bytes())
	return sum[:]
}
