package unitfile

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Instance distinguishes installations for separate state homes on one OS account.
func Instance(userHome, stateHome string) string {
	cleanHome := filepath.Clean(stateHome)
	if cleanHome == filepath.Join(filepath.Clean(userHome), ".sparkwing") {
		return ""
	}
	sum := sha256.Sum256([]byte(cleanHome))
	return hex.EncodeToString(sum[:4])
}

// Arguments reads the native argv; ambiguous systemd commands produce no arguments.
func Arguments(goos, body string) []string {
	if goos == "darwin" {
		return PlistArguments([]byte(body))
	}
	var args []string
	for _, line := range strings.Split(body, "\n") {
		if value, ok := strings.CutPrefix(line, "ExecStart="); ok {
			if args != nil {
				return nil
			}
			args = systemdWords(strings.TrimPrefix(value, ":"))
			if len(args) == 0 && strings.TrimSpace(value) != "" {
				return nil
			}
		}
	}
	return args
}

// Version reads the release recorded in the unit without executing its binary.
func Version(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(strings.TrimPrefix(line, "# "), "<!-- ")
		if value, ok := strings.CutPrefix(line, "SparkwingVersion="); ok {
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(value, " -->"))
			if err != nil {
				return ""
			}
			return string(decoded)
		}
	}
	return ""
}

// VersionRecord encodes release metadata without introducing forbidden XML-comment sequences.
func VersionRecord(goos, version string) string {
	value := "SparkwingVersion=" + base64.StdEncoding.EncodeToString([]byte(version))
	if goos == "darwin" {
		return "<!-- " + value + " -->"
	}
	return "# " + value
}

// Command encodes native systemd argv without introducing a shell.
func Command(args []string) string {
	words := make([]string, len(args))
	for i, word := range args {
		words[i] = SystemdWord(word)
	}
	// safety: the ':' prefix keeps dollar signs literal in both the executable and its argv.
	return ":" + strings.Join(words, " ")
}

// RewriteCommand replaces the argv and release while retaining captured environment and native policy.
func RewriteCommand(goos, body string, args []string, version string) (string, error) {
	if goos == "linux" {
		lines := strings.Split(body, "\n")
		for i, line := range lines {
			if value, ok := strings.CutPrefix(line, "ExecStart="); ok && strings.TrimSpace(value) != "" {
				lines[i] = "ExecStart=" + Command(args)
			}
			if strings.HasPrefix(line, "# SparkwingVersion=") {
				lines[i] = VersionRecord(goos, version)
			}
		}
		return strings.Join(lines, "\n"), nil
	}
	key := "<key>ProgramArguments</key>"
	start := strings.Index(body, key)
	if start < 0 {
		return "", errors.New("native unit: missing agent command")
	}
	start += len(key)
	end := strings.Index(body[start:], "</array>")
	if end < 0 {
		return "", errors.New("native unit: incomplete agent command")
	}
	var command strings.Builder
	command.WriteString("\n  <array>\n")
	for _, arg := range args {
		fmt.Fprintf(&command, "    <string>%s</string>\n", XMLText(arg))
	}
	command.WriteString("  ")
	body = body[:start] + command.String() + body[start+end:]
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "<!-- SparkwingVersion=") {
			lines[i] = VersionRecord(goos, version)
		}
	}
	return strings.Join(lines, "\n"), nil
}
