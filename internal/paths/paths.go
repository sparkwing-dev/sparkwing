package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type Paths struct {
	Root string
}

func UnderTest() bool {
	base := filepath.Base(os.Args[0])
	return strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".test.exe")
}

func TestSandbox() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("sparkwing-test-home-%d", os.Getpid()))
}

func DefaultPaths() (Paths, error) {
	if root := os.Getenv("SPARKWING_HOME"); root != "" {
		return PathsAt(root), nil
	}
	if UnderTest() {
		return PathsAt(filepath.Join(TestSandbox(), ".sparkwing")), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	return PathsAt(filepath.Join(home, ".sparkwing")), nil
}

func PathsAt(root string) Paths { return Paths{Root: root} }

func (p Paths) StateDB() string { return filepath.Join(p.Root, "state.db") }

func (p Paths) BoxSlotDir() string { return filepath.Join(p.Root, "box-slots") }

// StandaloneDir holds one runs store per store schema, each written by
// pipeline binaries that could not reach the admission daemon. The CLI's
// own verbs read [Paths.StateDB] and never look here.
func (p Paths) StandaloneDir() string { return filepath.Join(p.Root, "standalone") }

// StandaloneSchemaDir names the directory a binary expecting store schema
// version writes to. Binaries at neighboring schemas keep separate files,
// so one never migrates the file another is reading.
func (p Paths) StandaloneSchemaDir(version int) string {
	return filepath.Join(p.StandaloneDir(), fmt.Sprintf("schema-%d", version))
}

// StandaloneStateDB is the runs store this binary opens when a run cannot be
// hosted.
func (p Paths) StandaloneStateDB() string {
	return filepath.Join(p.StandaloneSchemaDir(store.ExpectedSchemaVersion()), "state.db")
}

// EnsureStandaloneDir prepares the directory holding [Paths.StandaloneStateDB]
// under the home's own mode.
func (p Paths) EnsureStandaloneDir() error {
	if err := p.EnsureRoot(); err != nil {
		return err
	}
	if err := fssecure.EnsureDir(p.StandaloneDir()); err != nil {
		return err
	}
	return fssecure.EnsureDir(p.StandaloneSchemaDir(store.ExpectedSchemaVersion()))
}

func (p Paths) ToolchainsDir() string { return filepath.Join(p.Root, "toolchains") }

func (p Paths) ToolchainDir(version string) string {
	return filepath.Join(p.ToolchainsDir(), version)
}

func (p Paths) ToolchainBinary(version string) string {
	name := "sparkwing"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(p.ToolchainDir(version), name)
}

func (p Paths) VersionStampDir() string { return filepath.Join(p.Root, "last-version.d") }

func (p Paths) VersionStampFile(key string) string {
	return filepath.Join(p.VersionStampDir(), key)
}

func (p Paths) RunsDir() string { return filepath.Join(p.Root, "runs") }

func (p Paths) RunDir(runID string) string {
	return filepath.Join(p.RunsDir(), runID)
}

func (p Paths) NodeLog(runID, nodeID string) string {
	return filepath.Join(p.RunDir(runID), sanitizeNodeFile(nodeID)+".log")
}

func (p Paths) EnvelopeLog(runID string) string {
	return filepath.Join(p.RunDir(runID), "_envelope.ndjson")
}

var reservedNodeFileChars = []string{"/", `\`, ":", "*", "?", `"`, "<", ">", "|"}

func sanitizeNodeFile(nodeID string) string {
	for _, c := range reservedNodeFileChars {
		nodeID = strings.ReplaceAll(nodeID, c, "__")
	}
	return nodeID
}

func (p Paths) EnsureRunDir(runID string) error {
	if err := p.EnsureRoot(); err != nil {
		return err
	}
	if err := fssecure.EnsureDir(p.RunsDir()); err != nil {
		return err
	}
	return fssecure.EnsureDir(p.RunDir(runID))
}

func (p Paths) EnsureRoot() error {
	return fssecure.EnsureDir(p.Root)
}
