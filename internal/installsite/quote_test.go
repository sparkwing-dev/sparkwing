package installsite

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRetireRemedy pins the guarded Unix action and undo. Both refuse an
// existing target, including a dangling symlink, so repeating either command
// cannot overwrite a file that appeared after the report was rendered.
func TestRetireRemedy(t *testing.T) {
	cases := []struct {
		goos, path, action, undo string
	}{
		{
			"linux", "/usr/local/bin/sparkwing",
			"test ! -e /usr/local/bin/sparkwing.superseded && test ! -L /usr/local/bin/sparkwing.superseded && mv -n -- /usr/local/bin/sparkwing /usr/local/bin/sparkwing.superseded && test ! -e /usr/local/bin/sparkwing && test ! -L /usr/local/bin/sparkwing",
			"test ! -e /usr/local/bin/sparkwing && test ! -L /usr/local/bin/sparkwing && mv -n -- /usr/local/bin/sparkwing.superseded /usr/local/bin/sparkwing && test ! -e /usr/local/bin/sparkwing.superseded && test ! -L /usr/local/bin/sparkwing.superseded",
		},
		{
			"darwin", "/Users/u/My Tools/sparkwing",
			"test ! -e '/Users/u/My Tools/sparkwing.superseded' && test ! -L '/Users/u/My Tools/sparkwing.superseded' && mv -n -- '/Users/u/My Tools/sparkwing' '/Users/u/My Tools/sparkwing.superseded' && test ! -e '/Users/u/My Tools/sparkwing' && test ! -L '/Users/u/My Tools/sparkwing'",
			"test ! -e '/Users/u/My Tools/sparkwing' && test ! -L '/Users/u/My Tools/sparkwing' && mv -n -- '/Users/u/My Tools/sparkwing.superseded' '/Users/u/My Tools/sparkwing' && test ! -e '/Users/u/My Tools/sparkwing.superseded' && test ! -L '/Users/u/My Tools/sparkwing.superseded'",
		},
		{
			"linux", "/home/u/a'b/sparkwing",
			`test ! -e '/home/u/a'\''b/sparkwing.superseded' && test ! -L '/home/u/a'\''b/sparkwing.superseded' && mv -n -- '/home/u/a'\''b/sparkwing' '/home/u/a'\''b/sparkwing.superseded' && test ! -e '/home/u/a'\''b/sparkwing' && test ! -L '/home/u/a'\''b/sparkwing'`,
			`test ! -e '/home/u/a'\''b/sparkwing' && test ! -L '/home/u/a'\''b/sparkwing' && mv -n -- '/home/u/a'\''b/sparkwing.superseded' '/home/u/a'\''b/sparkwing' && test ! -e '/home/u/a'\''b/sparkwing.superseded' && test ! -L '/home/u/a'\''b/sparkwing.superseded'`,
		},
		{
			"linux", "/home/u/$(reboot)/sparkwing",
			"test ! -e '/home/u/$(reboot)/sparkwing.superseded' && test ! -L '/home/u/$(reboot)/sparkwing.superseded' && mv -n -- '/home/u/$(reboot)/sparkwing' '/home/u/$(reboot)/sparkwing.superseded' && test ! -e '/home/u/$(reboot)/sparkwing' && test ! -L '/home/u/$(reboot)/sparkwing'",
			"test ! -e '/home/u/$(reboot)/sparkwing' && test ! -L '/home/u/$(reboot)/sparkwing' && mv -n -- '/home/u/$(reboot)/sparkwing.superseded' '/home/u/$(reboot)/sparkwing' && test ! -e '/home/u/$(reboot)/sparkwing.superseded' && test ! -L '/home/u/$(reboot)/sparkwing.superseded'",
		},
	}
	for _, c := range cases {
		got := retireRemedy(c.goos, c.path)
		if !got.Executable {
			t.Errorf("retireRemedy(%q, %q) is not executable", c.goos, c.path)
		}
		if got.Action != c.action {
			t.Errorf("retireRemedy(%q, %q).Action = %q, want %q", c.goos, c.path, got.Action, c.action)
		}
		if got.Undo != c.undo {
			t.Errorf("retireRemedy(%q, %q).Undo = %q, want %q", c.goos, c.path, got.Undo, c.undo)
		}
	}
}

// TestRetireRemedyRefusesACollision executes the rendered Unix command against
// real files. A pre-existing .superseded file is not an invitation to replace
// it, and once the collision is removed the same guidance remains reversible.
func TestRetireRemedyRefusesACollision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cash $ and quote'", "sparkwing")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("source"), 0o755); err != nil {
		t.Fatal(err)
	}
	destination := path + ".superseded"
	if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	remedy := retireRemedy("darwin", path)
	if err := exec.Command("sh", "-c", remedy.Action).Run(); err == nil {
		t.Fatal("retirement command overwrote or ignored an existing destination")
	}
	for file, want := range map[string]string{path: "source", destination: "existing"} {
		got, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q after collision, want %q", file, got, want)
		}
	}

	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	stubDir := filepath.Join(dir, "race-stub")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\ndestination=\"\"\nfor argument in \"$@\"; do destination=\"$argument\"; done\nprintf race >\"$destination\"\nexec /bin/mv \"$@\"\n"
	if err := os.WriteFile(filepath.Join(stubDir, "mv"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	race := exec.Command("sh", "-c", remedy.Action)
	race.Env = append(os.Environ(), "PATH="+stubDir+":/usr/bin:/bin")
	if out, err := race.CombinedOutput(); err == nil {
		t.Fatalf("retirement command hid a destination race: %s", out)
	}
	for file, want := range map[string]string{path: "source", destination: "race"} {
		got, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q after race, want %q", file, got, want)
		}
	}
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-c", remedy.Action).CombinedOutput(); err != nil {
		t.Fatalf("guarded retirement failed without a collision: %v: %s", err, out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("source still exists after retirement: %v", err)
	}
	if out, err := exec.Command("sh", "-c", remedy.Undo).CombinedOutput(); err != nil {
		t.Fatalf("guarded undo failed without a collision: %v: %s", err, out)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "source" {
		t.Fatalf("undo did not restore source: %q, %v", got, err)
	}
}

// TestRetireRemedyWindowsIsGuidanceNotAUniversalCommand covers legal Windows
// path characters that cmd.exe and PowerShell interpret differently. The path
// must be reported exactly, but Sparkwing must not offer an unsafe paste.
func TestRetireRemedyWindowsIsGuidanceNotAUniversalCommand(t *testing.T) {
	path := "C:\\Tools\\cash$ percent% bang! tick` amp& quote'\\sparkwing.exe"
	got := retireRemedy("windows", path)
	if got.Executable {
		t.Fatal("Windows retirement guidance claims to be a universal executable command")
	}
	text := got.Text()
	for _, want := range []string{path, path + ".superseded", "File Explorer", "cmd.exe", "PowerShell", "chosen shell"} {
		if !strings.Contains(text, want) {
			t.Errorf("Windows guidance missing %q: %s", want, text)
		}
	}
	for _, forbidden := range []string{"move ", "mv "} {
		if strings.Contains(text, forbidden) {
			t.Errorf("Windows guidance printed a purported universal command %q: %s", forbidden, text)
		}
	}
}

// TestShellQuote covers the quoting layer directly, including the empty
// string, which must still render as a shell word.
func TestShellQuote(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/plain/path", "/plain/path"},
		{"", "''"},
		{"a b", "'a b'"},
	}
	for _, c := range cases {
		if got := shellQuote(c.in); got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
