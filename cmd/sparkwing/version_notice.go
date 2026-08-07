// The run output is the only doc every agent reliably reads, so a CLI
// version change pushes a one-line pointer at the changelog instead of
// hoping the agent browses docs. The last-run version is stamped in the
// sparkwing home; the first invocation after the binary changes prints
// one stderr line (and surfaces the same line in `sparkwing info`),
// then never again for that transition.
//
// The stamp is per install, keyed by the running binary's resolved
// path. A machine with two sparkwing binaries on two different PATHs --
// the shell's and a launchd job's -- would otherwise have each rewrite
// a shared stamp with its own version on every run, and the record
// would read as a stream of upgrades and downgrades that never
// happened. Each install comparing only against what it itself last
// ran as makes the notice true no matter how many copies exist, without
// any binary having to know about, own, or touch the others.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

// pendingUpgradeNotice is set once per process by
// noteVersionTransition when the running binary differs from the
// stamped last-run version, so `sparkwing info` can render the same
// pointer the dispatcher emitted to stderr.
var pendingUpgradeNotice string

// upgradeNoticeLine is the one-line pointer emitted on a version
// transition. One line, with embedded-docs pointers only -- it must
// never grow into a nag.
func upgradeNoticeLine(prev, cur string) string {
	return fmt.Sprintf(
		"sparkwing changed %s -> %s: see `sparkwing docs read --topic %s`; "+
			"recovery controls: `sparkwing docs read --topic local-execution`",
		prev, cur, "changelog")
}

// versionTransition reports whether prev->cur is a transition worth
// announcing: both sides known and different. Unknown/missing versions
// (a fresh home, a first run of this install, a build without version
// metadata) never announce.
func versionTransition(prev, cur string) bool {
	if prev == "" || cur == "" {
		return false
	}
	if prev == "(unknown)" || cur == "(unknown)" {
		return false
	}
	return prev != cur
}

// noteVersionTransition reads the running install's last-run stamp,
// compares it to the running binary, and re-stamps. On a transition it
// records the pointer in pendingUpgradeNotice and, unless the verb owns
// its own rendering (info), writes the line to w. Best-effort: a
// read-only or absent home, or a binary whose own path will not
// resolve, never breaks dispatch.
func noteVersionTransition(w io.Writer, verb string) {
	if quietNoticeVerb(verb) {
		return
	}
	p, err := paths.DefaultPaths()
	if err != nil {
		return
	}
	self, err := installsite.Self()
	if err != nil {
		return
	}
	noteVersionTransitionForExe(w, verb, p, self)
}

func noteVersionTransitionForExe(w io.Writer, verb string, p paths.Paths, exe string) {
	stamp := p.VersionStampFile(installsite.PathKey(exe))
	prev := readVersionStamp(stamp)
	cur := installedVersion()
	writeVersionStamp(stamp, exe, cur)
	if !versionTransition(prev, cur) {
		return
	}
	line := upgradeNoticeLine(prev, cur)
	pendingUpgradeNotice = line
	if verb != "info" {
		fmt.Fprintln(w, line)
	}
}

// readVersionStamp returns the version recorded in file: the first line
// that is not blank and not a `#` comment, so the stamp can carry the
// binary path it describes as a note to the operator reading the file.
func readVersionStamp(file string) string {
	body, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}

func writeVersionStamp(file, exe, version string) {
	if version == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(file, []byte("# "+exe+"\n"+version+"\n"), 0o644)
}

// quietNoticeVerb suppresses the transition line for machine-facing
// verbs: shell completion, internal helpers, the trigger shim, and the
// daemons. Their output is parsed, not read.
func quietNoticeVerb(verb string) bool {
	if strings.HasPrefix(verb, "_") {
		return true
	}
	switch verb {
	case "completion", "handle-trigger", "wingd":
		return true
	}
	return false
}
