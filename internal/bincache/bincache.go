package bincache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

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

var errMissingGoSum = errors.New("missing go.sum entries")

type compileError struct {
	Output []byte
	Err    error
}

func (e *compileError) Error() string { return fmt.Sprintf("compile .sparkwing/: %v", e.Err) }
func (e *compileError) Unwrap() error { return e.Err }

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

// safety: compiler flags belong in the freshness key; remote digest verification cannot detect a stale build.
var pipelineBuildFlags = []string{"-trimpath", "-ldflags", "-s -w"}

// CompileOptions binds compiler subprocesses and diagnostics to captured inputs.
type CompileOptions struct {
	Environment  []string
	Diagnostics  io.Writer
	HashAllFiles bool
}

func pipelineBuildEnvironment(env []string) []string {
	// perf: an uncached compile with cgo builds runtime/cgo and the cgo variants of net and os/user,
	// 1-3s on a 2-vCPU CI runner; a pipeline that links C sets CGO_ENABLED=1 itself.
	if value, _ := gotoolchain.LookupEnv(env, "CGO_ENABLED"); value != "" || env == nil {
		return env
	}
	return append(append([]string(nil), env...), "CGO_ENABLED=0")
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
	options.Environment = pipelineBuildEnvironment(options.Environment)
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
	cmd := procenv.ControlCommand(env, tool, args...)
	cmd.Dir = sparkwingDir
	var captured lockedBuffer
	cmd.Stdout = io.MultiWriter(options.Diagnostics, &captured)
	cmd.Stderr = cmd.Stdout

	if err := gotoolchain.Run(ctx, cmd); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return err
		}
		if strings.Contains(captured.String(), "missing go.sum entry") {
			return errMissingGoSum
		}
		if explanation := gotoolchain.ExplainOutput(ctx, captured.String(), env); explanation != nil {
			return &compileError{Output: []byte(explanation.Error()), Err: explanation}
		}
		return &compileError{Output: captured.Bytes(), Err: err}
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
	return pipelineCacheKeyForPlatform(sparkwingDir, runtime.GOOS, runtime.GOARCH)
}

func pipelineCacheKeyForPlatform(sparkwingDir, goos, goarch string) (string, error) {
	parts, err := stableKeyParts(context.Background(), sparkwingDir, goos, goarch, os.Environ(), false)
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
	return explainCacheKey(context.Background(), sparkwingDir, env, false)
}

const stableKeyAttempts = 3

func explainCacheKey(ctx context.Context, sparkwingDir string, env []string, hashAllFiles bool) (string, []KeyPart, error) {
	parts, err := stableKeyParts(ctx, sparkwingDir, runtime.GOOS, runtime.GOARCH, env, hashAllFiles)
	if err != nil {
		return "", nil, err
	}
	return foldKey(parts), parts, nil
}

func stableKeyParts(ctx context.Context, sparkwingDir, goos, goarch string, env []string, hashAllFiles bool) ([]KeyPart, error) {
	for range stableKeyAttempts {
		parts, err := keyParts(ctx, sparkwingDir, goos, goarch, env, hashAllFiles)
		if !errors.Is(err, ErrUnstableInputs) {
			return parts, err
		}
	}
	return nil, ErrUnstableInputs
}

func keyParts(ctx context.Context, sparkwingDir, goos, goarch string, env []string, hashAllFiles bool) ([]KeyPart, error) {
	var parts []KeyPart
	add := func(label, detail string, material []byte) {
		parts = append(parts, KeyPart{
			Label: label, Detail: detail,
			Digest: digestOf(material), material: material,
		})
	}

	add("platform", goos+"/"+goarch, []byte(fmt.Sprintf("arch:%s/%s\n", goos, goarch)))

	flags := strings.Join(pipelineBuildFlags, " ")
	add("build flags", flags, []byte("buildflags:"+flags+"\n"))
	add("build environment", "captured Go build settings", buildEnvironmentKey(pipelineBuildEnvironment(env)))
	settings, compiled, err := effectiveBuildInputs(ctx, sparkwingDir, env)
	if err != nil {
		return nil, err
	}
	add("effective Go environment", "resolved toolchain and build settings", settings)
	add("compiled inputs", "Go-selected source and embedded files", compiled)

	var moduleBuf bytes.Buffer
	moduleStats, err := hashDirIntoCounted(&moduleBuf, sparkwingDir, allFiles, hashAllFiles)
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
		stats, err := hashDirIntoCounted(&buf, t.Dir, allFiles, hashAllFiles)
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

type hashStats struct {
	Files   int
	Bytes   int64
	Ignored int
}

func (s hashStats) String() string {
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

// ErrUnstableInputs reports build inputs that changed while hashing or compiling; the build must
// neither use nor store a cached binary.
var ErrUnstableInputs = errors.New("pipeline build inputs changed while the cache key was computed")

type fileState struct {
	size    int64
	modTime int64
}

func hashable(dir string, keep fileFilter, hashAllFiles bool, directories map[string]string) ([]string, int, error) {
	var prune, ignored map[string]bool
	if !hashAllFiles {
		prune, ignored = ignoredUnder(dir)
	}
	files, pruned, err := walkHashable(dir, keep, prune, directories)
	if err != nil || len(ignored) == 0 {
		return files, pruned, err
	}
	kept := files[:0]
	for _, path := range files {
		if ignored[path] {
			pruned++
		} else {
			kept = append(kept, path)
		}
	}
	return kept, pruned, nil
}

func snapshotHashable(files []string) (map[string]fileState, error) {
	out := make(map[string]fileState, len(files))
	for _, path := range files {
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrUnstableInputs
		}
		if err != nil {
			return nil, err
		}
		out[path] = fileState{size: info.Size(), modTime: info.ModTime().UnixNano()}
	}
	return out, nil
}

func hashDirIntoCounted(h io.Writer, dir string, keep fileFilter, hashAllFiles bool) (hashStats, error) {
	files, ignored, err := hashable(dir, keep, hashAllFiles, nil)
	if err != nil {
		return hashStats{}, err
	}
	before, err := snapshotHashable(files)
	if err != nil {
		return hashStats{}, err
	}
	// safety: Windows exposes no change time, so retain the extra enumeration there.
	if runtime.GOOS == "windows" {
		files, ignored, err = hashable(dir, keep, hashAllFiles, nil)
		if err != nil {
			return hashStats{}, err
		}
	}
	var digest bytes.Buffer
	stats, err := hashFilesInto(&digest, dir, files)
	stats.Ignored = ignored
	if err != nil {
		return stats, err
	}
	for range stableKeyAttempts {
		directories := make(map[string]string)
		files, _, err := hashable(dir, keep, hashAllFiles, directories)
		if err != nil {
			return stats, err
		}
		after, err := snapshotHashable(files)
		if err != nil {
			return stats, err
		}
		if !maps.Equal(before, after) {
			return stats, ErrUnstableInputs
		}
		unchanged, err := unchangedDirectories(directories)
		if err != nil {
			return stats, err
		}
		if runtime.GOOS == "windows" || unchanged {
			_, err = h.Write(digest.Bytes())
			return stats, err
		}
	}
	return stats, ErrUnstableInputs
}

// safety: a file created after its parent's enumeration must trigger another listing, including when
// an author restores the directory's modification time. Excluded churn may settle without changing the key.
func unchangedDirectories(before map[string]string) (bool, error) {
	for path, identity := range before {
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if identity == "" || directoryIdentity(info) != identity {
			return false, nil
		}
	}
	return true, nil
}

// perf: parallel hashing reduces key latency while ordered results keep the digest deterministic.
func hashFilesInto(h io.Writer, dir string, files []string) (hashStats, error) {
	var stats hashStats
	memo := openDigestMemo(dir)
	digests := make([][]byte, len(files))
	sizes := make([]int64, len(files))
	errs := make([]error, len(files))
	var next sync.Mutex
	index := 0
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), 8, len(files)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				next.Lock()
				i := index
				index++
				next.Unlock()
				if i >= len(files) {
					return
				}
				rel, err := filepath.Rel(dir, files[i])
				if err != nil {
					errs[i] = err
					continue
				}
				digests[i], sizes[i], errs[i] = hashFile(files[i], rel, memo)
			}
		}()
	}
	wg.Wait()
	for i, path := range files {
		if errs[i] != nil {
			if errors.Is(errs[i], fs.ErrPermission) {
				refusal := &apierr.Error{Code: apierr.BadRequest, Message: fmt.Sprintf("cannot read pipeline cache input %q", path), Hint: "Restore read access for the current user, then repeat the command"}
				return stats, fmt.Errorf("%w: %w", refusal, errs[i])
			}
			return stats, errs[i]
		}
		rel, _ := filepath.Rel(dir, path)
		stats.Files++
		stats.Bytes += sizes[i]
		fmt.Fprintf(h, "%s\x00%x\n", rel, digests[i])
	}
	memo.save()
	return stats, nil
}

func hashFile(path, rel string, memo *digestMemo) ([]byte, int64, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, ErrUnstableInputs
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	sum, identity, found := memo.lookup(rel, info)
	if found {
		return sum, info.Size(), nil
	}
	fileH := sha256.New()
	n, err := io.Copy(fileH, f)
	if err != nil {
		return nil, 0, err
	}
	sum = fileH.Sum(nil)
	memo.record(rel, identity, sum)
	return sum, n, nil
}

// perf: wholly ignored directories in prune are never walked; each counts as one excluded path.
func walkHashable(dir string, keep fileFilter, prune map[string]bool, directories map[string]string) ([]string, int, error) {
	var files []string
	pruned := 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path != dir && errors.Is(err, fs.ErrNotExist) {
				return ErrUnstableInputs
			}
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", ".claude-scratch":
				return filepath.SkipDir
			}
			if prune[path] {
				pruned++
				return filepath.SkipDir
			}
			if directories != nil && runtime.GOOS != "windows" {
				info, err := d.Info()
				if err != nil {
					if errors.Is(err, fs.ErrNotExist) {
						return ErrUnstableInputs
					}
					return err
				}
				directories[path] = directoryIdentity(info)
			}
			return nil
		}
		// safety: a linked worktree's .git file names that worktree's own path, so
		// hashing it gives every checkout of one commit its own key.
		if d.Name() == ".git" {
			return nil
		}
		if !keep(d.Name()) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	return files, pruned, err
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

var buildEnvironmentNames = strings.Fields("GOVERSION GOOS GOARCH GO386 GOAMD64 GOARM GOARM64 GOMIPS GOMIPS64 GOPPC64 GORISCV64 GOWASM GOEXPERIMENT GOFIPS140 GOFLAGS GOTOOLCHAIN GOROOT GOPATH GOSUMDB GONOSUMDB GOINSECURE GOPRIVATE GONOPROXY CGO_ENABLED CC CXX FC AR PKG_CONFIG CGO_CFLAGS CGO_CPPFLAGS CGO_CXXFLAGS CGO_FFLAGS CGO_LDFLAGS")

func buildEnvironmentKey(env []string) []byte {
	var input bytes.Buffer
	for _, name := range buildEnvironmentNames {
		value, _ := gotoolchain.LookupEnv(env, name)
		fmt.Fprintf(&input, "%s=%s\x00", name, value)
	}
	// perf: compiler identities detect tool upgrades without invalidating binaries for unrelated PATH changes.
	tools := []string{"go", "gcc", "clang", "pkg-config"}
	for _, name := range []string{"CC", "CXX"} {
		if value, _ := gotoolchain.LookupEnv(env, name); len(strings.Fields(value)) > 0 {
			tools = append(tools, strings.Fields(value)[0])
		}
	}
	for _, tool := range tools {
		fmt.Fprintf(&input, "tool:%s=%s\x00", tool, toolIdentity(env, tool))
	}
	sum := sha256.Sum256(input.Bytes())
	return sum[:]
}

func toolIdentity(env []string, tool string) string {
	candidates := []string{tool}
	if !filepath.IsAbs(tool) {
		candidates = nil
		path, _ := gotoolchain.LookupEnv(env, "PATH")
		for _, dir := range filepath.SplitList(path) {
			if dir == "" {
				continue
			}
			name := filepath.Join(dir, tool)
			if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
				name += ".exe"
			}
			candidates = append(candidates, name)
		}
	}
	for _, candidate := range candidates {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil || info.IsDir() || runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
			continue
		}
		return fmt.Sprintf("%s|%d|%d", resolved, info.Size(), info.ModTime().UnixNano())
	}
	return "absent"
}
