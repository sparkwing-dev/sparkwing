package main

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/installsite"
)

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
