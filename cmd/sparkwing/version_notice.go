// The run output is the only doc every agent reliably reads, so a CLI
// version change pushes a one-line pointer at the changelog instead of
// hoping the agent browses docs. The last-run version is stamped in the
// sparkwing home; the first invocation after the binary changes prints
// one stderr line (and surfaces the same line in `sparkwing info`),
// then never again for that transition.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

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
		"sparkwing upgraded %s -> %s: see `sparkwing docs read --topic %s`; "+
			"recovery controls: `sparkwing docs read --topic local-execution`",
		prev, cur, "changelog")
}

// versionTransition reports whether prev->cur is a transition worth
// announcing: both sides known and different. Unknown/missing versions
// (a fresh home, a build without version metadata) never announce.
func versionTransition(prev, cur string) bool {
	if prev == "" || cur == "" {
		return false
	}
	if prev == "(unknown)" || cur == "(unknown)" {
		return false
	}
	return prev != cur
}

// noteVersionTransition reads the last-run stamp, compares it to the
// running binary, and stamps the current version. On a transition it
// records the pointer in pendingUpgradeNotice and, unless the verb owns
// its own rendering (info), writes the line to w. Best-effort: a
// read-only or absent home never breaks dispatch.
func noteVersionTransition(w io.Writer, verb string) {
	if quietNoticeVerb(verb) {
		return
	}
	p, err := paths.DefaultPaths()
	if err != nil {
		return
	}
	prev := readVersionStamp(p)
	cur := installedVersion()
	writeVersionStamp(p, cur)
	if !versionTransition(prev, cur) {
		return
	}
	line := upgradeNoticeLine(prev, cur)
	pendingUpgradeNotice = line
	if verb != "info" {
		fmt.Fprintln(w, line)
	}
}

func readVersionStamp(p paths.Paths) string {
	body, err := os.ReadFile(p.LastVersionFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

func writeVersionStamp(p paths.Paths, version string) {
	if version == "" {
		return
	}
	if err := p.EnsureRoot(); err != nil {
		return
	}
	_ = os.WriteFile(p.LastVersionFile(), []byte(version+"\n"), 0o644)
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
