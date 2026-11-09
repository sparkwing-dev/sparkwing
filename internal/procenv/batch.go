package procenv

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// safety: Win32 path normalization drops trailing dots and spaces, so the trimmed name is the file cmd.exe runs.
func batchScript(path string) (string, bool) {
	script := strings.TrimRight(path, " .")
	extension := filepath.Ext(script)
	return script, strings.EqualFold(extension, ".cmd") || strings.EqualFold(extension, ".bat")
}

const batchRemedy = "use a non-batch executable, such as node.exe with the tool's JavaScript entrypoint"

// safety: mirrors Rust's CVE-2024-24576 fix, make_bat_command_line and append_bat_arg in
// https://github.com/rust-lang/rust/blob/master/library/std/src/sys/args/windows.rs; quoting keeps & | < > ^ ( )
// literal, /v:OFF keeps !VAR!, and CR, LF and NUL end the cmd.exe line, so only those are refused.
func batchCommandLine(script string, args []string) (string, error) {
	if strings.ContainsAny(script, "\"^%\r\n\x00") || strings.HasSuffix(script, `\`) {
		return "", fmt.Errorf("windows batch command refused: argv[0] path contains a quote, ^, %%, CR, LF or NUL or ends with a backslash; rename the batch file or %s", batchRemedy)
	}
	var line strings.Builder
	line.WriteString(`cmd.exe /e:ON /v:OFF /d /c ""`)
	line.WriteString(script)
	line.WriteByte('"')
	for i, arg := range args {
		if strings.ContainsAny(arg, "\r\n\x00") {
			return "", fmt.Errorf("windows batch command refused: argv[%d] contains CR, LF or NUL, which cmd.exe cannot pass to a batch file; %s", i+1, batchRemedy)
		}
		line.WriteByte(' ')
		appendBatchArgument(&line, arg)
	}
	line.WriteByte('"')
	return line.String(), nil
}

func appendBatchArgument(line *strings.Builder, arg string) {
	quote := arg == "" || strings.HasSuffix(arg, `\`)
	for _, r := range arg {
		if r <= unicode.MaxASCII && !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(`#$*+-./:?@\_`, r)) || unicode.IsControl(r) {
			quote = true
			break
		}
	}
	if quote {
		line.WriteByte('"')
	}
	backslashes := 0
	for _, r := range arg {
		switch r {
		case '\\':
			backslashes++
			line.WriteByte('\\')
			continue
		case '"':
			// safety: "" keeps cmd.exe's quote state and doubled backslashes keep the MSVCRT parse literal.
			line.WriteString(strings.Repeat(`\`, backslashes))
			line.WriteByte('"')
		case '%':
			// safety: cmd.exe expands %cd:~,% to nothing, so a following %VAR% stays literal even inside quotes.
			line.WriteString("%%cd:~,")
		}
		backslashes = 0
		line.WriteRune(r)
	}
	if quote {
		line.WriteString(strings.Repeat(`\`, backslashes))
		line.WriteByte('"')
	}
}
