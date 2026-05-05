package sparks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/mod/modfile"
)

// OverlayModfileName is the generated overlay modfile. Gitignored.
const OverlayModfileName = ".resolved.mod"

// OverlaySumfileName is the sum file written by `go mod download` using
// the overlay modfile. Gitignored.
const OverlaySumfileName = ".resolved.sum"

// goModFilename is the consumer's git-tracked modfile. NEVER modified by
// this package.
const goModFilename = "go.mod"

// goBin returns the `go` binary to invoke; tests may override via
// SPARKS_GO_BIN.
func goBin() string {
	if v := os.Getenv("SPARKS_GO_BIN"); v != "" {
		return v
	}
	return "go"
}

// WriteOverlay generates .resolved.mod + .resolved.sum in sparkwingDir
// from the consumer's go.mod plus the resolved-version map. If the
// overlay already matches the desired output, returns (false, nil)
// without writing.
//
// The consumer's go.mod and go.sum are never modified. `go mod download
// -modfile=.resolved.mod` is invoked to materialize .resolved.sum unless
// there are no require edits needed.
func WriteOverlay(ctx context.Context, sparkwingDir string, resolved map[string]string) (bool, error) {
	if sparkwingDir == "" {
		return false, errors.New("sparks: sparkwingDir must not be empty")
	}
	goModPath := filepath.Join(sparkwingDir, goModFilename)
	raw, err := os.ReadFile(goModPath)
	if err != nil {
		return false, fmt.Errorf("sparks: read %s: %w", goModPath, err)
	}
	// Fingerprint go.mod so we can assert later it is unchanged.
	beforeGoMod := append([]byte(nil), raw...)

	overlayBytes, err := buildOverlay(raw, goModPath, resolved)
	if err != nil {
		return false, err
	}

	overlayPath := filepath.Join(sparkwingDir, OverlayModfileName)
	sumPath := filepath.Join(sparkwingDir, OverlaySumfileName)

	// Fast path: if the overlay file already exists and matches the
	// desired bytes exactly, skip regeneration.
	existing, err := os.ReadFile(overlayPath)
	if err == nil && bytes.Equal(existing, overlayBytes) {
		if err := ensureGitignore(sparkwingDir); err != nil {
			return false, err
		}
		if err := assertGoModUntouched(goModPath, beforeGoMod); err != nil {
			return false, err
		}
		return false, nil
	}

	if err := os.WriteFile(overlayPath, overlayBytes, 0o644); err != nil {
		return false, fmt.Errorf("sparks: write overlay: %w", err)
	}

	// Materialize .resolved.sum via `go mod download`. Skip when no
	// resolved versions were provided (nothing to download).
	if len(resolved) > 0 {
		if err := materializeSum(ctx, sparkwingDir, overlayPath); err != nil {
			return false, err
		}
	} else {
		// Ensure the sum file at least exists (touch) so callers can
		// rely on both files being present when the overlay is present.
		if _, err := os.Stat(sumPath); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(sumPath, nil, 0o644); err != nil {
				return false, fmt.Errorf("sparks: touch sum: %w", err)
			}
		}
	}

	if err := ensureGitignore(sparkwingDir); err != nil {
		return false, err
	}
	if err := assertGoModUntouched(goModPath, beforeGoMod); err != nil {
		return false, err
	}
	return true, nil
}

// buildOverlay returns the contents for .resolved.mod given raw go.mod
// bytes and a resolved-version map. The file is the user's go.mod with
// matching `require` entries rewritten to the resolved versions. If a
// resolved module is not in go.mod, a new require line is appended.
func buildOverlay(rawGoMod []byte, goModPath string, resolved map[string]string) ([]byte, error) {
	f, err := modfile.Parse(goModPath, rawGoMod, nil)
	if err != nil {
		return nil, fmt.Errorf("sparks: parse go.mod: %w", err)
	}
	// Track which resolved entries already have a require line so we can
	// append the rest.
	seen := make(map[string]bool, len(resolved))
	for _, req := range f.Require {
		if newVer, ok := resolved[req.Mod.Path]; ok {
			seen[req.Mod.Path] = true
			if req.Mod.Version == newVer {
				continue
			}
			if err := f.AddRequire(req.Mod.Path, newVer); err != nil {
				return nil, fmt.Errorf("sparks: AddRequire %s: %w", req.Mod.Path, err)
			}
		}
	}
	for mod, ver := range resolved {
		if seen[mod] {
			continue
		}
		if err := f.AddRequire(mod, ver); err != nil {
			return nil, fmt.Errorf("sparks: AddRequire %s: %w", mod, err)
		}
	}
	f.Cleanup()
	formatted, err := f.Format()
	if err != nil {
		return nil, fmt.Errorf("sparks: format overlay: %w", err)
	}
	return formatted, nil
}

func materializeSum(ctx context.Context, workDir, overlayPath string) error {
	// `go mod download -modfile=X` without a pattern only resolves
	// the modules explicitly listed in X -- not their transitive
	// closure. Building the pipeline binary with `-modfile=X` then
	// fails with "missing go.sum entry" for every indirect. Passing
	// the `all` meta-pattern forces the transitive walk so the sum
	// file covers everything the build actually compiles against.
	cmd := exec.CommandContext(ctx, goBin(), "mod", "download",
		"-modfile="+overlayPath, "all")
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sparks: go mod download -modfile=%s all: %w: %s",
			overlayPath, err, string(out))
	}
	return nil
}

func assertGoModUntouched(path string, before []byte) error {
	after, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("sparks: re-read go.mod: %w", err)
	}
	if !bytes.Equal(before, after) {
		return fmt.Errorf("sparks: go.mod was modified during overlay generation; this is a bug in internal/sparks")
	}
	return nil
}

// ensureGitignore appends the overlay entries to .gitignore if missing.
// Walks up from sparkwingDir looking for an existing .gitignore; if none
// exists, writes one next to the sparkwing dir.
func ensureGitignore(sparkwingDir string) error {
	// Prefer a .gitignore at or above sparkwingDir. Start with the repo
	// root if we can find one (look for .git); otherwise fall back to
	// sparkwingDir itself.
	target := locateGitignore(sparkwingDir)
	entry := filepath.Join(filepath.Base(sparkwingDir), OverlayModfileName)
	sumEntry := filepath.Join(filepath.Base(sparkwingDir), OverlaySumfileName)
	// The consumer may have invoked us with a path that is not literally
	// ".sparkwing". We write both the literal-path entry and the glob
	// `.sparkwing/.resolved.*` when the dir is named .sparkwing so the
	// more common case is covered.
	var lines []string
	if filepath.Base(sparkwingDir) == ".sparkwing" {
		lines = []string{".sparkwing/.resolved.*"}
	} else {
		lines = []string{entry, sumEntry}
	}

	existing, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sparks: read .gitignore: %w", err)
	}
	current := string(existing)
	var missing []string
	for _, line := range lines {
		if !gitignoreContains(current, line) {
			missing = append(missing, line)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	var buf bytes.Buffer
	if len(existing) > 0 {
		buf.Write(existing)
		if !bytes.HasSuffix(existing, []byte("\n")) {
			buf.WriteByte('\n')
		}
	}
	buf.WriteString("\n# sparks overlay modfile (generated, do not commit)\n")
	for _, line := range missing {
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(target, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("sparks: write .gitignore: %w", err)
	}
	return nil
}

// locateGitignore finds the best .gitignore to update. Walks up from
// sparkwingDir looking for a `.git` sibling; the .gitignore sits next to
// it. Falls back to sparkwingDir/.gitignore.
func locateGitignore(sparkwingDir string) string {
	dir := sparkwingDir
	for range 10 {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return filepath.Join(dir, ".gitignore")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return filepath.Join(sparkwingDir, ".gitignore")
}

func gitignoreContains(gitignore, line string) bool {
	for _, l := range bytes.Split([]byte(gitignore), []byte("\n")) {
		if string(bytes.TrimSpace(l)) == line {
			return true
		}
	}
	return false
}
