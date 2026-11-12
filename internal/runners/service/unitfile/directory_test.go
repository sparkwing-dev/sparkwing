package unitfile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRewriteWorkingDirectoryPreservesNativeSettings(t *testing.T) {
	home := filepath.Join(t.TempDir(), "state % & quotes\"")
	for _, tc := range []struct{ name, goos, body, want string }{
		{"linux-add", "linux", "[Unit]\nDescription=captured\n[Service]\nExecStart=/bin/true\nEnvironment=WorkingDirectory=captured\n[Install]\nWantedBy=default.target\n", "[Service]\nWorkingDirectory=" + SystemdWord(home) + "\nExecStart="},
		{"linux-replace", "linux", "[Service]\nWorkingDirectory=/old\nExecStart=/bin/true\nEnvironment=WorkingDirectory=captured\n", "WorkingDirectory=" + SystemdWord(home) + "\n"},
		{"plist-add", "darwin", `<plist><dict><key>ProgramArguments</key><array><string>/bin/true</string></array><key>EnvironmentVariables</key><dict><key>WorkingDirectory</key><string>captured</string></dict></dict></plist>`, "<key>WorkingDirectory</key><string>" + XMLText(home) + "</string>"},
		{"plist-replace", "darwin", `<plist><dict><key>WorkingDirectory</key><!-- captured comment --><string>/old</string><key>ProgramArguments</key><array><string>/bin/true</string></array><key>EnvironmentVariables</key><dict><key>WorkingDirectory</key><string>captured</string></dict></dict></plist>`, "<key>WorkingDirectory</key><!-- captured comment --><string>" + XMLText(home) + "</string>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RewriteWorkingDirectory(tc.goos, tc.body, home)
			if err != nil || !strings.Contains(got, tc.want) || !strings.Contains(got, "captured") {
				t.Fatalf("rewrite=%s error=%v", got, err)
			}
			if tc.goos == "darwin" && !strings.Contains(got, "<key>WorkingDirectory</key><string>captured</string>") {
				t.Fatal("changed captured environment key instead of root directory")
			}
			second, err := RewriteWorkingDirectory(tc.goos, got, home)
			if err != nil || second != got {
				t.Fatalf("rewrite is not idempotent: %s %v", second, err)
			}
		})
	}
}

func TestRewriteWorkingDirectoryRefusesUnusableNativeInput(t *testing.T) {
	prefix := `<plist><dict><key>ProgramArguments</key><array><string>/bin/true</string></array>`
	for _, tc := range []struct{ goos, body, home string }{
		{"linux", "[Service]\nExecStart=/bin/true\n", "relative"},
		{"linux", "[Unit]\nExecStart=/bin/true\n", t.TempDir()},
		{"darwin", prefix + `<key>WorkingDirectory</key><integer>1</integer></dict></plist>`, t.TempDir()},
		{"darwin", prefix + `<key>WorkingDirectory</key><string>/one</string><key>WorkingDirectory</key><string>/two</string></dict></plist>`, t.TempDir()},
	} {
		if _, err := RewriteWorkingDirectory(tc.goos, tc.body, tc.home); err == nil {
			t.Fatalf("accepted invalid %s body or home: %s %s", tc.goos, tc.body, tc.home)
		}
	}
}

func TestPrepareWorkingDirectoryKeepsExistingPermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if err := PrepareWorkingDirectory(root); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(root, 0o751); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := PrepareWorkingDirectory(root); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode() != after.Mode() {
		t.Fatal("existing permissions changed")
	}
	path := filepath.Join(root, "occupied")
	if err := os.WriteFile(path, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareWorkingDirectory(path); err == nil {
		t.Fatal("regular file accepted as working directory")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "retained" {
		t.Fatal("existing file changed")
	}
}
