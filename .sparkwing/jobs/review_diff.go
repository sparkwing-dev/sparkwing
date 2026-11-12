package jobs

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func reviewGit(ctx context.Context, root string, args ...string) (string, error) {
	result, err := sparkwing.Exec(ctx, "git", args...).Dir(root).Capture()
	return result.Stdout, err
}

// safety: Git's binary heuristic permits non-UTF-8 text; inspect both blobs before sending a patch to a reviewer.
func reviewDiff(ctx context.Context, d reviewDeps, base, head string, files []string) (string, error) {
	paths := []string{}
	if len(files) > 0 {
		paths = append([]string{"--"}, files...)
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
		binary := false
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
			binary = binary || !utf8.ValidString(body)
			metadata[side] = fmt.Sprintf("size=%d sha256=%x", len(body), sha256.Sum256([]byte(body)))
		}
		if binary {
			fmt.Fprintf(&summary, "Binary file %q\n  base: %s\n  head: %s\n", path, metadata[0], metadata[1])
			excluded = append(excluded, ":(top,exclude,literal)"+path)
		}
	}
	if len(excluded) > 0 {
		if len(paths) == 0 {
			paths = []string{"--", ":/"}
		}
		paths = append(paths, excluded...)
	}
	diff, err := d.git(ctx, append([]string{"diff", "--no-color", "--no-ext-diff", base, head}, paths...)...)
	if err != nil {
		return "", err
	}
	return summary.String() + diff, nil
}
