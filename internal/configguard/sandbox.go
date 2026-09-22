package configguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

// ErrOutsideSandboxHome reports a config write refused because it would have
// landed outside the sparkwing home the command runs under, which is the
// operator's own config directory in every case that matters.
var ErrOutsideSandboxHome = errors.New("config write would leave the sparkwing home")

// GuardWrite refuses a write to path when the command runs under a sparkwing
// home that is not the operator's own ~/.sparkwing and path sits outside it.
// what names the file in the refusal, as in "profiles lives at". override names
// the environment variable that moves the file: a set override passes the
// guard, because that is the operator naming the file, and the refusal reports
// the value that keeps the write inside the home. A file no variable moves
// takes an empty override, and its caller adds the remedy it does have.
func GuardWrite(what, override, path string) error {
	if override != "" && os.Getenv(override) != "" {
		return nil
	}
	home, ok := paths.SandboxHome()
	if !ok || fssecure.UnderDir(home, path) {
		return nil
	}
	// safety: a test binary's sandbox root holds both its home and the config
	// redirect beside it, so a write landing there has already stayed inside
	// the sandbox and reaches neither the operator's files nor a fake HOME.
	if paths.UnderTest() && fssecure.UnderDir(paths.TestSandbox(), path) {
		return nil
	}
	if override == "" {
		return fmt.Errorf("%w: this command runs under the sparkwing home %s, but %s lives at %s, "+
			"which SPARKWING_HOME does not move. Clear SPARKWING_HOME to write the machine's copy on purpose",
			ErrOutsideSandboxHome, home, what, path)
	}
	return fmt.Errorf("%w: this command runs under the sparkwing home %s, but %s lives at %s, "+
		"which SPARKWING_HOME does not move. Point %s at %s to keep the write inside this home, "+
		"or clear SPARKWING_HOME to write the machine's copy on purpose",
		ErrOutsideSandboxHome, home, what, path, override, filepath.Join(home, filepath.Base(path)))
}
