package procenv

import (
	"strings"
	"testing"
)

//sparkwing:critical
func TestBatchCommandLine(t *testing.T) {
	const script = `C:\Program Files (x86) & Tools\tool.cmd`
	const prefix = `cmd.exe /e:ON /v:OFF /d /c ""` + script + `" `
	for _, tc := range []struct{ arg, want string }{
		{"plain", `plain`},
		{"", `""`},
		{`C:\dir\file.txt`, `C:\dir\file.txt`},
		{"#$*+-./:?@_", `#$*+-./:?@_`},
		{"héllo", `héllo`},
		{"日本語", `日本語`},
		{"with spaces", `"with spaces"`},
		{"tab\there", "\"tab\there\""},
		{"esc\x1b", "\"esc\x1b\""},
		{"next\u0085line", "\"next\u0085line\""},
		{"--flag=value", `"--flag=value"`},
		{"a&b", `"a&b"`},
		{"main&ver&rem", `"main&ver&rem"`},
		{"a|b<c>d", `"a|b<c>d"`},
		{"x^y", `"x^y"`},
		{"(x)", `"(x)"`},
		{"!x!", `"!x!"`},
		{"%PATH%", `"%%cd:~,%PATH%%cd:~,%"`},
		{"100%", `"100%%cd:~,%"`},
		{`"q"`, `"""q"""`},
		{`a"&calc&"b`, `"a""&calc&""b"`},
		{`a\`, `"a\\"`},
		{`a\\`, `"a\\\\"`},
		{`a\"b`, `"a\\""b"`},
		{`a\b c\`, `"a\b c\\"`},
	} {
		got, err := batchCommandLine(script, []string{tc.arg})
		if err != nil || got != prefix+tc.want+`"` {
			t.Errorf("argument %q:\n got %s, %v\nwant %s", tc.arg, got, err, prefix+tc.want+`"`)
		}
	}
	got, err := batchCommandLine(`C:\t\npm.cmd`, []string{"run", "a&b", "%X%"})
	if want := `cmd.exe /e:ON /v:OFF /d /c ""C:\t\npm.cmd" run "a&b" "%%cd:~,%X%%cd:~,%""`; err != nil || got != want {
		t.Fatalf("several arguments: %s, %v; want %s", got, err, want)
	}
}

//sparkwing:critical
func TestBatchCommandLineRefusesOnlyUnescapable(t *testing.T) {
	for _, arg := range []string{"x\ry", "x\ny", "x\x00y", "\r\n"} {
		_, err := batchCommandLine(`C:\t\tool.cmd`, []string{"safe", arg})
		if err == nil || !strings.Contains(err.Error(), "argv[2]") || !strings.Contains(err.Error(), "non-batch executable") || strings.Contains(err.Error(), arg) {
			t.Errorf("argument %q: %v", arg, err)
		}
	}
	for _, script := range []string{`C:\a"b\tool.cmd`, `C:\100%\tool.cmd`, `C:\a^b\tool.cmd`, "C:\\a\nb\\tool.cmd", `C:\t\`} {
		_, err := batchCommandLine(script, []string{"plain"})
		if err == nil || !strings.Contains(err.Error(), "argv[0]") || !strings.Contains(err.Error(), "non-batch executable") {
			t.Errorf("script %q: %v", script, err)
		}
	}
}

func TestBatchScript(t *testing.T) {
	for _, tc := range []struct {
		path, script string
		batch        bool
	}{
		{`C:\t\tool.cmd`, `C:\t\tool.cmd`, true},
		{`C:\t\tool.BAT`, `C:\t\tool.BAT`, true},
		{`C:\t\tool.CmD. `, `C:\t\tool.CmD`, true},
		{`C:\t\tool.exe`, "", false},
		{`C:\t\tool.cmd.exe`, "", false},
		{`C:\t\cmd`, "", false},
	} {
		script, batch := batchScript(tc.path)
		if batch != tc.batch || batch && script != tc.script {
			t.Errorf("batchScript(%q) = %q, %v", tc.path, script, batch)
		}
	}
}
