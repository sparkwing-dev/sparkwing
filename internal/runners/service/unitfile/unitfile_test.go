package unitfile

import (
	"slices"
	"strings"
	"testing"
)

func TestSystemdCommandPreservesLiteralArguments(t *testing.T) {
	args := []string{"/bin with space/sparkwing", "__tick", "--home", "/state/$USER/100%/quote\"/newline\n", ""}
	command := Command(args)
	if !strings.HasPrefix(command, ":") || !strings.Contains(command, "$USER") {
		t.Fatal("systemd will substitute the installing account's environment into argv")
	}
	if strings.Contains(command, "\n") || !slices.Equal(Arguments("linux", "ExecStart="+command), args) {
		t.Fatalf("command lost literal arguments: %q -> %#v", command, Arguments("linux", "ExecStart="+command))
	}
	for _, malformed := range []string{`"unfinished`, `trailing\`, `"bad\q"`} {
		if words := SystemdWords(malformed); words != nil {
			t.Fatalf("accepted ambiguous native command %q: %#v", malformed, words)
		}
	}
}

func TestPlistArgumentsRefuseAmbiguousAndIncompleteDocuments(t *testing.T) {
	args := `<key>ProgramArguments</key><array><string>/sparkwing</string><string>__tick</string></array>`
	for _, body := range []string{
		`<plist><dict>` + args + args + `</dict></plist>`,
		`<plist><dict>` + args,
		`<plist><dict><key>ProgramArguments</key><array><integer>123</integer></array></dict></plist>`,
	} {
		if got := PlistArguments([]byte(body)); got != nil {
			t.Fatalf("accepted ambiguous native command: %#v", got)
		}
	}
}
