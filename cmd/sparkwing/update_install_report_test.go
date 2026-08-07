package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/installsite"
)

func TestGoInstallBinPath(t *testing.T) {
	sep := string(filepath.ListSeparator)
	cases := []struct {
		gobin, gopath, want string
	}{
		{"/opt/gobin", "/home/u/go", filepath.Join("/opt/gobin", installsite.ExeName())},
		{"", "/home/u/go", filepath.Join("/home/u/go", "bin", installsite.ExeName())},
		{"", "/first/go" + sep + "/second/go", filepath.Join("/first/go", "bin", installsite.ExeName())},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := goInstallBinPath(c.gobin, c.gopath); got != c.want {
			t.Errorf("goInstallBinPath(%q, %q) = %q, want %q", c.gobin, c.gopath, got, c.want)
		}
	}
}

// TestGoInstallBinTargetFromEnv pins the parse of raw `go env GOBIN
// GOPATH` output, not just the resolution of already-split values. The
// default machine -- GOBIN unset -- answers with a LEADING EMPTY LINE,
// and a whole-output TrimSpace used to swallow it, so the two-line
// answer read as one line and the target came back empty on exactly
// the configuration `go install` documents: first GOPATH element's bin.
func TestGoInstallBinTargetFromEnv(t *testing.T) {
	exe := installsite.ExeName()
	cases := []struct {
		name, out, want string
	}{
		{"gobin unset: leading empty line", "\n/home/u/go\n", filepath.Join("/home/u/go", "bin", exe)},
		{"gobin unset: crlf", "\r\n/home/u/go\r\n", filepath.Join("/home/u/go", "bin", exe)},
		{"gobin set", "/opt/gobin\n/home/u/go\n", filepath.Join("/opt/gobin", exe)},
		{"both unset", "\n\n", ""},
		{"empty output", "", ""},
		{"single line", "/home/u/go", ""},
	}
	for _, c := range cases {
		if got := goInstallBinTargetFromEnv(c.out); got != c.want {
			t.Errorf("%s: goInstallBinTargetFromEnv(%q) = %q, want %q", c.name, c.out, got, c.want)
		}
	}
}

// TestReportGoInstallOutcome_NamesTheInstalledPath is the go-install
// fallback half of BW-1675: `go install` drops the new build in GOBIN,
// not over the running binary, and the fallback used to end without
// saying so -- a second install created silently. The report must name
// the exact file installed and, when the running binary was not the one
// replaced, say that and give the remedy.
func TestReportGoInstallOutcome_NamesTheInstalledPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	installed := filepath.Join(dir, installsite.ExeName())
	if err := os.WriteFile(installed, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	self := otherInstall(t)

	var buf bytes.Buffer
	reportGoInstallOutcome(&buf, installed, self)
	out := buf.String()
	if !strings.Contains(out, "installed to "+installed) {
		t.Fatalf("fallback did not name the installed path:\n%s", out)
	}
	if !strings.Contains(out, self+" was not replaced") &&
		!strings.Contains(out, "("+self+") was not replaced") {
		t.Fatalf("fallback did not say the running binary survives:\n%s", out)
	}
	if !strings.Contains(out, installsite.RetireRemedy(self).Text()) {
		t.Fatalf("fallback did not give the exact remedy:\n%s", out)
	}
}

// TestReportGoInstallOutcome_QuotesARemedyPathWithSpaces: the remedy is
// a command the operator pastes, so a self path with a space must come
// out quoted -- bare, `mv` would read it as extra arguments and rename
// the wrong files.
func TestReportGoInstallOutcome_QuotesARemedyPathWithSpaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("asserts the POSIX-quoted form")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	installed := filepath.Join(dir, installsite.ExeName())
	if err := os.WriteFile(installed, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	self := filepath.Join(dir, "My Tools", installsite.ExeName())
	if err := os.MkdirAll(filepath.Dir(self), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(self, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	reportGoInstallOutcome(&buf, installed, self)
	want := "test ! -e '" + self + ".superseded' && test ! -L '" + self + ".superseded' && mv -n -- '" + self + "' '" + self + ".superseded' && test ! -e '" + self + "' && test ! -L '" + self + "'"
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("remedy for a path with spaces is not paste-safe; want %q in:\n%s", want, buf.String())
	}
}

// TestReportGoInstallOutcome_QuietWhenItReplacedTheRunner covers the
// case where the caller was already running the GOBIN copy: nothing to
// warn about.
func TestReportGoInstallOutcome_QuietWhenItReplacedTheRunner(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	installed := filepath.Join(dir, installsite.ExeName())
	if err := os.WriteFile(installed, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	reportGoInstallOutcome(&buf, installed, installed)
	if strings.Contains(buf.String(), "was not replaced") {
		t.Fatalf("replacing the running binary still warned:\n%s", buf.String())
	}
}

// TestWriteOtherInstallsNote gives every competing copy an exact,
// reversible remedy and never claims failure: reporting is all update
// is entitled to do about a binary somebody else installed.
func TestWriteOtherInstallsNote(t *testing.T) {
	var quiet bytes.Buffer
	writeOtherInstallsNote(&quiet, "/dest/sparkwing", nil)
	if quiet.Len() != 0 {
		t.Fatalf("no competing installs still produced a note: %q", quiet.String())
	}

	other := installsite.Copy{Path: "/home/u/go/bin/sparkwing", ModTime: time.Date(2026, 8, 2, 0, 50, 0, 0, time.UTC)}
	var buf bytes.Buffer
	writeOtherInstallsNote(&buf, "/dest/sparkwing", []installsite.Copy{other})
	out := buf.String()
	for _, want := range []string{
		other.Path,
		installsite.RetireRemedy(other.Path).Text(),
		"/dest/sparkwing",
		"sparkwing doctor",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("note missing %q:\n%s", want, out)
		}
	}
}

// TestWriteOtherInstallsNote_QuotesPathsWithSpaces holds update's note
// to the same paste-safety as doctor's: a competing copy under a
// directory with a space gets a quoted remedy.
func TestWriteOtherInstallsNote_QuotesPathsWithSpaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("asserts the POSIX-quoted form")
	}
	other := installsite.Copy{Path: "/home/u/My Tools/sparkwing", ModTime: time.Date(2026, 8, 2, 0, 50, 0, 0, time.UTC)}
	var buf bytes.Buffer
	writeOtherInstallsNote(&buf, "/dest/sparkwing", []installsite.Copy{other})
	want := "test ! -e '/home/u/My Tools/sparkwing.superseded' && test ! -L '/home/u/My Tools/sparkwing.superseded' && mv -n -- '/home/u/My Tools/sparkwing' '/home/u/My Tools/sparkwing.superseded' && test ! -e '/home/u/My Tools/sparkwing' && test ! -L '/home/u/My Tools/sparkwing'"
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("note remedy is not paste-safe; want %q in:\n%s", want, buf.String())
	}
}
