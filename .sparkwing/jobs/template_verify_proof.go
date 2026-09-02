package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	templates "github.com/sparkwing-dev/sparks-core/templates"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: proofFormat sits in the digest so that widening or narrowing the
// input set invalidates every recorded proof instead of silently reusing one
// that a different set of inputs produced.
const proofFormat = 1

type proofEnv struct {
	Sparkwing  string `json:"sparkwing"`
	SparksCore string `json:"sparks_core"`
	Toolchain  string `json:"toolchain"`

	Reusable bool   `json:"reusable"`
	Reason   string `json:"reason,omitempty"`
}

type proofInputs struct {
	Format     int    `json:"format"`
	Template   string `json:"template"`
	Content    string `json:"content"`
	Manifest   string `json:"manifest"`
	Sparkwing  string `json:"sparkwing"`
	SparksCore string `json:"sparks_core"`
	Toolchain  string `json:"toolchain"`
	Tools      string `json:"tools"`
}

func (p proofInputs) digest() (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func resolveProofEnv(ctx context.Context, root string, core map[string]string, exhaustive bool) proofEnv {
	if exhaustive {
		return proofEnv{Reason: "exhaustive proof requested"}
	}
	if len(core) == 0 {
		return proofEnv{Reason: "no local sparks-core checkout, so the published module versions a scaffold resolves are not part of any digest"}
	}
	tree, err := checkoutDigest(ctx, root)
	if err != nil {
		return proofEnv{Reason: fmt.Sprintf("cannot digest the sparkwing checkout: %v", err)}
	}
	coreTree, err := checkoutDigest(ctx, sparksCoreRoot(root))
	if err != nil {
		return proofEnv{Reason: fmt.Sprintf("cannot digest the sparks-core checkout: %v", err)}
	}
	chain, err := toolchainDigest(ctx)
	if err != nil {
		return proofEnv{Reason: fmt.Sprintf("cannot digest the Go toolchain: %v", err)}
	}
	return proofEnv{Sparkwing: tree, SparksCore: coreTree, Toolchain: chain, Reusable: true}
}

func templateProofDigest(ctx context.Context, env proofEnv, m templates.Manifest) (string, error) {
	if !env.Reusable {
		return "", fmt.Errorf("proof inputs are incomplete: %s", env.Reason)
	}
	content, err := templateContentDigest(m.Name)
	if err != nil {
		return "", err
	}
	manifest, err := manifestVerifyDigest(m)
	if err != nil {
		return "", err
	}
	tools, err := templateToolsDigest(ctx, m)
	if err != nil {
		return "", err
	}
	return proofInputs{
		Format:     proofFormat,
		Template:   m.Name,
		Content:    content,
		Manifest:   manifest,
		Sparkwing:  env.Sparkwing,
		SparksCore: env.SparksCore,
		Toolchain:  env.Toolchain,
		Tools:      tools,
	}.digest()
}

func templateContentDigest(name string) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(templates.FS, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		body, err := fs.ReadFile(templates.FS, p)
		if err != nil {
			return err
		}
		hashField(h, path.Clean(p), body)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("digest template %s: %w", name, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func manifestVerifyDigest(m templates.Manifest) (string, error) {
	tools := append([]string(nil), m.VerifyTools...)
	sort.Strings(tools)
	body, err := json.Marshal(struct {
		Name    string            `json:"name"`
		Tier    string            `json:"tier"`
		Fixture string            `json:"fixture"`
		Params  map[string]string `json:"params"`
		Tools   []string          `json:"tools"`
	}{m.Name, m.Tier(), m.Fixture(), m.VerifyParams, tools})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func templateToolsDigest(ctx context.Context, m templates.Manifest) (string, error) {
	h := sha256.New()
	for _, tool := range sortedUnique(append(fixtureTools(m.Fixture()), m.VerifyTools...)) {
		hashField(h, tool, []byte(toolIdentity(ctx, tool)))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fixtureTools(fixture string) []string {
	switch fixture {
	case templates.FixtureDocker, templates.FixturePostgres:
		return []string{"docker"}
	case templates.FixtureNodeModule:
		return []string{"node", "npm"}
	case templates.FixturePythonModule:
		return []string{"python3"}
	case templates.FixtureGoModule:
		return []string{"git"}
	default:
		return nil
	}
}

func toolIdentity(ctx context.Context, tool string) string {
	bin, err := exec.LookPath(tool)
	if err != nil {
		return toolReachability(ctx, tool, "absent")
	}
	res, err := sparkwing.Exec(ctx, bin, "--version").Capture()
	if err != nil {
		return toolReachability(ctx, tool, "present:"+bin)
	}
	return toolReachability(ctx, tool, bin+":"+strings.TrimSpace(res.Stdout))
}

// safety: the docker binary being on PATH says nothing about the daemon the
// run step actually needs, so the same probe the run step gates on goes into
// the digest. Without it a proof taken while the daemon was down would be
// reused once it came up.
func toolReachability(ctx context.Context, tool, identity string) string {
	if tool != "docker" {
		return identity
	}
	return identity + ":daemon=" + strconv.FormatBool(dockerAvailable(ctx))
}

func checkoutDigest(ctx context.Context, dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("no checkout directory")
	}
	head, err := gitField(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	diff, err := gitField(ctx, dir, "diff", "--binary", "HEAD")
	if err != nil {
		return "", err
	}
	untracked, err := gitField(ctx, dir, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	hashField(h, "head", []byte(head))
	hashField(h, "diff", []byte(diff))
	for _, rel := range sortedLines(untracked) {
		body, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return "", fmt.Errorf("read untracked %s: %w", rel, err)
		}
		hashField(h, "untracked:"+rel, body)
	}
	for _, rel := range ignoredBuildInputs {
		if err := hashPathOrAbsent(h, rel, filepath.Join(dir, rel)); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// safety: git ignores both of these and both change what gets verified. go.work
// steers the plain `go build` that produces the verify CLI and the sparks-core
// discovery that pins a scaffold's modules; internal/web/next-out is embedded
// into that CLI.
var ignoredBuildInputs = []string{"go.work", filepath.Join("internal", "web", "next-out")}

func hashPathOrAbsent(h io.Writer, name, target string) error {
	info, err := os.Stat(target)
	if errors.Is(err, os.ErrNotExist) {
		hashField(h, name, []byte("absent"))
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", name, err)
	}
	if !info.IsDir() {
		body, err := os.ReadFile(target)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		hashField(h, name, body)
		return nil
	}
	var rels []string
	if err := filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(target, p)
		if relErr != nil {
			return relErr
		}
		rels = append(rels, rel)
		return nil
	}); err != nil {
		return fmt.Errorf("walk %s: %w", name, err)
	}
	sort.Strings(rels)
	hashField(h, name, fmt.Appendf(nil, "dir:%d", len(rels)))
	for _, rel := range rels {
		body, err := os.ReadFile(filepath.Join(target, rel))
		if err != nil {
			return fmt.Errorf("read %s/%s: %w", name, rel, err)
		}
		hashField(h, name+":"+filepath.ToSlash(rel), body)
	}
	return nil
}

func toolchainDigest(ctx context.Context) (string, error) {
	res, err := sparkwing.Exec(ctx, "go", "env", "GOVERSION", "GOOS", "GOARCH").Capture()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(res.Stdout))
	return hex.EncodeToString(sum[:]), nil
}

func gitField(ctx context.Context, dir string, args ...string) (string, error) {
	res, err := sparkwing.Exec(ctx, "git", args...).Dir(dir).Capture()
	if err != nil {
		return "", fmt.Errorf("git %s in %s: %w", strings.Join(args, " "), dir, err)
	}
	return res.Stdout, nil
}

func hashField(h io.Writer, name string, body []byte) {
	fmt.Fprintf(h, "%s\x00%d\x00", name, len(body))
	_, _ = h.Write(body)
}

func sortedLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	sort.Strings(lines)
	return lines
}

func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func proofDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "sparkwing", "template-verify-proofs"), nil
}

const proofRetention = 14 * 24 * time.Hour

type proofRecord struct {
	Format     int       `json:"format"`
	Template   string    `json:"template"`
	Tier       string    `json:"tier"`
	RanRunStep bool      `json:"ran_run_step"`
	RecordedAt time.Time `json:"recorded_at"`
}

func proofRecorded(dir, digest string) bool {
	if digest == "" || dir == "" {
		return false
	}
	body, err := os.ReadFile(filepath.Join(dir, digest))
	if err != nil {
		return false
	}
	var rec proofRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return false
	}
	return rec.Format == proofFormat
}

func recordProof(dir, digest string, rec proofRecord) error {
	if digest == "" {
		return errors.New("refusing to record a proof without a digest")
	}
	rec.Format = proofFormat
	rec.RecordedAt = time.Now().UTC()
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	pruneProofs(dir, time.Now())
	tmp, err := os.CreateTemp(dir, digest+".*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, digest))
}

func pruneProofs(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= proofRetention {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}
