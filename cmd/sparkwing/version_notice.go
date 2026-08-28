package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

var pendingUpgradeNotice string

func upgradeNoticeLine(prev, cur string) string {
	return fmt.Sprintf(
		"sparkwing changed %s -> %s: see `sparkwing docs read --topic %s`; "+
			"recovery controls: `sparkwing docs read --topic local-execution`",
		prev, cur, "changelog")
}

func versionTransition(prev, cur string) bool {
	if prev == "" || cur == "" {
		return false
	}
	if prev == "(unknown)" || cur == "(unknown)" {
		return false
	}
	return prev != cur
}

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
	if err := fssecure.EnsureDir(filepath.Dir(file)); err != nil {
		return
	}
	_ = fssecure.WriteFile(file, []byte("# "+exe+"\n"+version+"\n"))
}

func quietNoticeVerb(verb string) bool {
	if strings.HasPrefix(verb, "_") {
		return true
	}
	switch verb {
	case "completion", "doctor", "handle-trigger", "wingd":
		return true
	}
	return false
}
