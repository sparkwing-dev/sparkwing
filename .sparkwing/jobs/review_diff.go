package jobs

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func reviewGit(ctx context.Context, root string, args ...string) (string, error) {
	result, err := sparkwing.Exec(ctx, "git", append([]string{"-c", "core.quotePath=true"}, args...)...).Dir(root).Capture()
	return result.Stdout, err
}

// safety: Git's binary heuristic permits non-UTF-8 text; inspect both blobs before sending a patch to a reviewer.
func reviewDiff(ctx context.Context, d reviewDeps, base, head string, files []string) (string, error) {
	paths := []string{}
	if len(files) > 0 {
		paths = []string{"--"}
		for _, file := range files {
			paths = append(paths, ":(top,literal)"+file)
		}
	}
	raw, err := d.git(ctx, append([]string{"diff", "--raw", "--no-abbrev", "--no-renames", "-z", "--no-ext-diff", base, head}, paths...)...)
	if err != nil {
		return "", err
	}
	records := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	var summary strings.Builder
	var excluded []string
	for i := 0; i < len(records) && records[i] != ""; i += 2 {
		if i+1 >= len(records) {
			return "", fmt.Errorf("review: malformed raw diff")
		}
		fields := strings.Fields(records[i])
		if len(fields) != 5 || !strings.HasPrefix(fields[0], ":") {
			return "", fmt.Errorf("review: malformed raw diff metadata")
		}
		path := records[i+1]
		binary, invalid := false, false
		metadata := make([]string, 2)
		for side := range 2 {
			mode := strings.TrimPrefix(fields[side], ":")
			if mode == "000000" {
				metadata[side] = "absent"
				continue
			}
			if mode == "160000" {
				metadata[side] = "gitlink " + fields[side+2]
				continue
			}
			body, err := d.git(ctx, "cat-file", "blob", fields[side+2])
			if err != nil {
				return "", err
			}
			binary = binary || reviewBinaryBlob(body)
			invalid = invalid || !utf8.ValidString(body)
			metadata[side] = fmt.Sprintf("blob=%s mode=%s size=%d sha256=%x\n    inspect: git cat-file blob %s | od -An -tx1 -v", fields[side+2], mode, len(body), sha256.Sum256([]byte(body)), fields[side+2])
		}
		if binary {
			fmt.Fprintf(&summary, "Binary file %q\n  UNREVIEWED CONTENT: bytes omitted; inspection required before approval.\n  base: %s\n  head: %s\n", path, metadata[0], metadata[1])
		} else if invalid {
			patch, err := d.git(ctx, "diff", "--no-color", "--no-renames", "--no-ext-diff", "--no-textconv", "--text", base, head, "--", ":(top,literal)"+path)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&summary, "Text file %q contains invalid UTF-8; bytes escaped\nPatch encoding: \\\\ decodes to one literal backslash; \\xNN decodes to one byte. Other UTF-8 is unchanged.\n%s", path, reviewEscapeInvalidBytes(patch))
		}
		if binary || invalid {
			excluded = append(excluded, ":(top,exclude,literal)"+path)
		}
	}
	if len(excluded) > 0 {
		if len(paths) == 0 {
			paths = []string{"--", ":/"}
		}
		paths = append(paths, excluded...)
	}
	diff, err := d.git(ctx, append([]string{"diff", "--no-color", "--no-renames", "--no-ext-diff", "--no-textconv", "--text", base, head}, paths...)...)
	if err != nil {
		return "", err
	}
	return summary.String() + diff, nil
}

// safety: a stray encoding byte must not hide executable text; only NUL or over 30% non-text bytes omit a patch.
func reviewBinaryBlob(body string) bool {
	if strings.IndexByte(body, 0) >= 0 {
		return true
	}
	nonText := 0
	for rest := body; rest != ""; {
		r, size := utf8.DecodeRuneInString(rest)
		if r == utf8.RuneError && size == 1 || unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' && r != '\f' {
			nonText += size
		}
		rest = rest[size:]
	}
	return nonText*10 > len(body)*3
}

func reviewEscapeInvalidBytes(text string) string {
	var escaped strings.Builder
	for text != "" {
		r, size := utf8.DecodeRuneInString(text)
		if r == '\\' {
			escaped.WriteString(`\\`)
		} else if r == utf8.RuneError && size == 1 {
			fmt.Fprintf(&escaped, "\\x%02x", text[0])
		} else {
			escaped.WriteString(text[:size])
		}
		text = text[size:]
	}
	return escaped.String()
}
