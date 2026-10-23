package unitfile

import (
	"encoding/base64"
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
		if words := systemdWords(malformed); words != nil {
			t.Fatalf("accepted ambiguous native command %q: %#v", malformed, words)
		}
	}
}

func FuzzSystemdArgumentsRejectIncompleteCommands(f *testing.F) {
	f.Add([]byte("credentials $USER 100%"))
	f.Add([]byte{0, 0xff, '\n'})
	f.Fuzz(func(t *testing.T, value []byte) {
		args := []string{"/bin/sparkwing", "start", "--config", base64.StdEncoding.EncodeToString(value), ""}
		valid := "ExecStart=" + Command(args) + "\n"
		if got := Arguments("linux", valid); !slices.Equal(got, args) {
			t.Fatalf("literal arguments changed: %#v != %#v", got, args)
		}
		for _, reset := range []string{"", " \t"} {
			if got := Arguments("linux", "ExecStart="+reset+"\n"+valid); !slices.Equal(got, args) {
				t.Fatalf("leading command reset changed arguments: %#v != %#v", got, args)
			}
		}
		for _, command := range []string{":", `"unfinished`, `trailing\`, `"bad\q"`, Command(args)} {
			other := "ExecStart=" + command + "\n"
			for _, body := range []string{other + valid, valid + other} {
				if got := Arguments("linux", body); got != nil {
					t.Fatalf("accepted ambiguous or incomplete command %q: %#v", body, got)
				}
			}
		}
	})
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

func FuzzPlistArguments(f *testing.F) {
	f.Add([]byte("owner's <config> & paths"))
	f.Add([]byte{0, 0xff, '\n'})
	f.Fuzz(func(t *testing.T, value []byte) {
		argument := base64.StdEncoding.EncodeToString(value) + "<&>\"'\n"
		args := []string{"/bin with space/sparkwing", "--config", argument, ""}
		var command strings.Builder
		command.WriteString(`<key>ProgramArguments</key><array>`)
		for _, arg := range args {
			command.WriteString("<string>" + XMLText(arg) + "</string>")
		}
		command.WriteString(`</array>`)
		body := "<plist><dict><key>Label</key><string>runner</string>" + command.String() + "</dict></plist>"
		if got := PlistArguments([]byte(body)); !slices.Equal(got, args) {
			t.Fatalf("native arguments changed: %#v != %#v", got, args)
		}
		for _, malformed := range []string{
			"<plist><dict>" + command.String() + command.String() + "</dict></plist>",
			"<plist><dict>" + command.String(),
			body + body,
		} {
			if got := PlistArguments([]byte(malformed)); got != nil {
				t.Fatalf("ambiguous or incomplete command accepted: %#v", got)
			}
		}
	})
}
