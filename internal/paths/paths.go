package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
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
