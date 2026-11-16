package jobs

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func reviewGit(ctx context.Context, root string, args ...string) (string, error) {
	result, err := sparkwing.Exec(ctx, "git", append([]string{"-c", "core.quotePath=true"}, args...)...).Dir(root).Capture()
	return result.Stdout, err
}

const binaryReviewOwner = "content-inspection"

type reviewDiffResult struct {
	text    string
	binary  []reviewBinaryChange
	missing []string
	parts   map[string]string
	order   []string
}

type reviewBinaryChange struct {
	path                string
	executable, shebang bool
}

// safety: model replies and cached verdicts cannot clear an omitted-content block.
func (d reviewDiffResult) enforceBinaryReview(rec *reviewRecord, p reviewPolicy, changes [][]string) {
	rec.Findings = slices.DeleteFunc(rec.Findings, func(f reviewFinding) bool { return f.Reviewer == binaryReviewOwner })
	for _, file := range d.missing {
		rec.Findings = append(rec.Findings, reviewFinding{
			ID: fmt.Sprintf("M%x", sha256.Sum256([]byte(file))), Severity: "P1", Rule: "missing-diff-review", File: file,
			Summary: "changed file has no rendered diff; requires human review: " + file,
			Status:  findingOpen, Reviewer: binaryReviewOwner,
		})
	}

	for _, file := range d.binary {
		strict := len(p.strictFor(file.path)) > 0
		for _, change := range changes {
			if !slices.Contains(change, file.path) {
				continue
			}
			for _, endpoint := range change {
				strict = strict || len(p.strictFor(endpoint)) > 0
			}
		}
		if !strict && !file.executable && !file.shebang && reviewAssetPath(file.path) {
			continue
		}
		rec.Findings = append(rec.Findings, reviewFinding{
			ID:       fmt.Sprintf("B%x", sha256.Sum256([]byte(file.path))),
			Severity: "P2", Rule: "binary-content-review", File: file.path,
			Summary: "binary or unreadable change requires human review: " + file.path,
			Status:  findingOpen, Reviewer: binaryReviewOwner,
		})
	}
}

func reviewAssetPath(file string) bool {
	switch strings.ToLower(path.Ext(file)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".svgz", ".woff", ".woff2", ".ttf", ".otf", ".pdf", ".zip", ".gz", ".tgz":
		return true
	}
	return false
}

// safety: Git's binary heuristic permits non-UTF-8 text; inspect both blobs before sending a patch to a reviewer.
func reviewDiff(ctx context.Context, d reviewDeps, base, head string) (reviewDiffResult, error) {
	result := reviewDiffResult{parts: map[string]string{}}
	raw, err := d.git(ctx, "diff", "--raw", "--no-abbrev", "--no-renames", "-z", "--no-ext-diff", base, head)
	if err != nil {
		return reviewDiffResult{}, err
	}
	diff, err := d.git(ctx, "diff", "--no-color", "--no-renames", "--no-ext-diff", "--no-textconv", "--text", "--src-prefix=a/", "--dst-prefix=b/", "--submodule=short", base, head)
	if err != nil {
		return reviewDiffResult{}, err
	}
	patches, err := reviewPatchFiles(diff)
	if err != nil {
		return reviewDiffResult{}, err
	}
	records := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	for i := 0; i < len(records) && records[i] != ""; i += 2 {
		if i+1 >= len(records) {
			return reviewDiffResult{}, fmt.Errorf("review: malformed raw diff")
		}
		fields := strings.Fields(records[i])
		if len(fields) != 5 || !strings.HasPrefix(fields[0], ":") {
			return reviewDiffResult{}, fmt.Errorf("review: malformed raw diff metadata")
		}
		path := records[i+1]
		result.order = append(result.order, path)
		var part strings.Builder
		patch, rendered := patches[path]
		if !rendered {
			result.missing = append(result.missing, path)
			fmt.Fprintf(&part, "Changed file %q has no rendered diff; requires human review.\n", path)
		}
		binary, invalid := false, false
		change := reviewBinaryChange{path: path}
		metadata := make([]string, 2)
		for side := range 2 {
			mode := strings.TrimPrefix(fields[side], ":")
			change.executable = change.executable || mode == "100755"
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
				return reviewDiffResult{}, err
			}
			change.shebang = change.shebang || strings.HasPrefix(body, "#!")
			binary = binary || reviewBinaryBlob(body)
			invalid = invalid || !utf8.ValidString(body)
			metadata[side] = fmt.Sprintf("blob=%s mode=%s size=%d sha256=%x\n    inspect: git cat-file blob %s | od -An -tx1 -v", fields[side+2], mode, len(body), sha256.Sum256([]byte(body)), fields[side+2])
		}
		if binary {
			result.binary = append(result.binary, change)
			fmt.Fprintf(&part, "Binary file %q\n  UNREVIEWED CONTENT: bytes omitted; inspection required before approval.\n  base: %s\n  head: %s\n", path, metadata[0], metadata[1])
		} else if invalid {
			fmt.Fprintf(&part, "Text file %q contains invalid UTF-8; bytes escaped\nPatch encoding: \\\\ decodes to one literal backslash; \\xNN decodes to one byte. Other UTF-8 is unchanged.\n%s", path, reviewEscapeInvalidBytes(patch))
		} else {
			part.WriteString(patch)
		}
		result.parts[path] = part.String()
	}
	return result, nil
}

// safety: the routing inventory must remain covered even if raw metadata is missing.
func (d *reviewDiffResult) requireChangedPaths(files []string) {
	for _, file := range files {
		if _, exists := d.parts[file]; exists {
			continue
		}
		d.missing = append(d.missing, file)
		d.order = append(d.order, file)
		d.parts[file] = fmt.Sprintf("Changed file %q has no rendered diff; requires human review.\n", file)
	}
	d.text = d.scoped(d.order)
}

// safety: filenames stay in byte-preserving Git output; JSON argv never carries raw filename bytes.
func (d reviewDiffResult) scoped(files []string) string {
	var out strings.Builder
	selected := make(map[string]bool, len(files))
	for _, file := range files {
		selected[file] = true
	}
	for _, path := range d.order {
		if selected[path] {
			out.WriteString(d.parts[path])
		}
	}
	return out.String()
}

func reviewPatchFiles(diff string) (map[string]string, error) {
	patches := map[string]string{}
	for diff != "" {
		header, _, ok := strings.Cut(diff, "\n")
		if !ok || !strings.HasPrefix(header, "diff --git ") {
			return nil, fmt.Errorf("review: malformed patch header")
		}
		names := strings.TrimPrefix(header, "diff --git ")
		// safety: --no-renames and pinned a/b prefixes give equal-length names, including C-quoted names with spaces.
		half := len(names) / 2
		if len(names)%2 != 1 || names[half] != ' ' {
			return nil, fmt.Errorf("review: malformed patch paths")
		}
		paths := []string{names[:half], names[half+1:]}
		for i, path := range paths {
			if strings.HasPrefix(path, "\"") {
				decoded, err := strconv.Unquote(path)
				if err != nil {
					return nil, fmt.Errorf("review: malformed quoted patch path: %w", err)
				}
				paths[i] = decoded
			}
		}
		if !strings.HasPrefix(paths[0], "a/") || !strings.HasPrefix(paths[1], "b/") || paths[0][2:] != paths[1][2:] || len(paths[0]) == 2 {
			return nil, fmt.Errorf("review: patch paths do not name one changed file")
		}
		path := paths[0][2:]
		end := len(diff)
		if next := strings.Index(diff, "\ndiff --git "); next >= 0 {
			end = next + 1
		}
		// safety: a file type change can produce deletion and addition patches for the same raw path.
		patches[path] += diff[:end]
		diff = diff[end:]
	}
	return patches, nil
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

// safety: NUL-delimited status records preserve raw names and both endpoints of renames and copies.
func reviewChangedPaths(ctx context.Context, d reviewDeps, base, head string) ([][]string, []string, error) {
	names, err := d.git(ctx, "diff", "--name-status", "-z", "--find-renames", "--find-copies", "--find-copies-harder", "--no-ext-diff", base, head)
	if err != nil {
		return nil, nil, fmt.Errorf("review: list changed files: %w", err)
	}
	var changes [][]string
	var modified []string
	for names != "" {
		status, rest, ok := strings.Cut(names, "\x00")
		if !ok || status == "" {
			return nil, nil, fmt.Errorf("review: malformed name-status diff")
		}
		names = rest
		count := 1
		if status[0] == 'R' || status[0] == 'C' {
			count = 2
		}
		var change []string
		for range count {
			file, rest, ok := strings.Cut(names, "\x00")
			if !ok || file == "" {
				return nil, nil, fmt.Errorf("review: malformed name-status path")
			}
			names = rest
			change = append(change, file)
		}
		changes = append(changes, change)
		// safety: an unchanged copy source affects strict routing but has no patch of its own.
		if status[0] == 'C' {
			modified = append(modified, change[1])
		} else {
			modified = append(modified, change...)
		}
	}
	return changes, modified, nil
}
