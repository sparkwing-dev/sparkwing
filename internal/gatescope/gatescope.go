// Package gatescope answers the two questions every source-policy checker in
// this repository asks: which files do I read, and which of their lines does
// this change own. A checker keeps its own judgment and its own report; the
// walk and the diff arithmetic live here so two checkers cannot drift into
// scoping the same commit differently.
package gatescope

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/gitenv"
)

var skipDirs = map[string]bool{
	"vendor":          true,
	"testdata":        true,
	"node_modules":    true,
	".git":            true,
	".claude-scratch": true,
}

// SkipDir reports whether a directory holds code no source gate judges.
func SkipDir(name string) bool {
	return skipDirs[name]
}

// Walk calls visit for every file under root whose path ends in suffix,
// skipping the directories SkipDir names. It hands visit the walked path and
// that path relative to root in slash form, which is the spelling git uses and
// the one a baseline file records.
func Walk(root, suffix string, visit func(path, rel string)) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if SkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, suffix) {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		visit(path, filepath.ToSlash(rel))
		return nil
	})
}

// AddedLines reports the lines a change adds, keyed by the path git names them
// under. staged reads the index the commit is being built in; otherwise the
// scope is the fork point from base, which also charges every untracked file
// the pathspec matches, because git diff alone leaves those out. The pathspec
// is a git pathspec such as *.go.
func AddedLines(root string, staged bool, base, pathspec string) (map[string]map[int]bool, error) {
	// safety: git quotes a path holding a non-ASCII byte unless core.quotePath
	// is off, and a quoted +++ header drops that file from the scope unjudged.
	args := []string{"-c", "core.quotePath=false", "diff", "--unified=0", "--no-color"}
	var index string
	if staged {
		index = stagedIndex()
		args = append(args, "--cached")
	} else {
		forkPoint := base
		if out, err := git(root, "", "merge-base", base, "HEAD"); err == nil {
			forkPoint = strings.TrimSpace(out)
		}
		args = append(args, forkPoint)
	}
	args = append(args, "--", pathspec)

	diff, err := git(root, index, args...)
	if err != nil {
		return nil, err
	}
	added := parseAddedLines(diff)
	if !staged {
		if err := addUntracked(root, pathspec, added); err != nil {
			return nil, err
		}
	}
	return added, nil
}

// Added reports whether the scope charges this line of this file to the change.
// rel is the path relative to the walked root; an absolute path is made
// relative to it first.
func Added(added map[string]map[int]bool, root, path string, line int) bool {
	return added[relativeTo(root, path)][line]
}

// Touched reports whether the scope names the file at all. A change that only
// deletes lines adds none, and it is the one most likely to break a parser.
func Touched(added map[string]map[int]bool, root, path string) bool {
	_, ok := added[relativeTo(root, path)]
	return ok
}

// DiffFailure is what a checker prints when it could not compute its scope, so
// the run gated nothing. tool names the binary in the message and its fix line.
func DiffFailure(tool, base string, err error) string {
	scope := "the staged diff"
	if base != "" {
		scope = base
	}
	return fmt.Sprintf("%s: cannot compute the diff against %s (%v), so nothing was gated.\n"+
		"Fix: fetch the base ref and name it, for example `git fetch origin main` then "+
		"`%s -base origin/main <root>`.\n"+
		"Pass -allow-no-diff to accept a run that gates nothing.", tool, scope, err, tool)
}

func relativeTo(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func addUntracked(root, pathspec string, added map[string]map[int]bool) error {
	out, err := git(root, "", "ls-files", "--others", "--exclude-standard", "-z", "--", pathspec)
	if err != nil {
		return err
	}
	for rel := range strings.SplitSeq(out, "\x00") {
		if rel == "" {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(root, rel))
		if rerr != nil {
			if os.IsNotExist(rerr) {
				continue
			}
			return rerr
		}
		set := added[rel]
		if set == nil {
			set = map[int]bool{}
			added[rel] = set
		}
		for line := 1; line <= strings.Count(string(data), "\n")+1; line++ {
			set[line] = true
		}
	}
	return nil
}

func stagedIndex() string {
	if os.Getenv("GIT_INDEX_FILE") != "" {
		return ""
	}
	return gitenv.GateIndex()
}

var hunkRE = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

func parseAddedLines(diff string) map[string]map[int]bool {
	added := map[string]map[int]bool{}
	var cur string
	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			cur = strings.TrimPrefix(line, "+++ b/")
		case strings.HasPrefix(line, "+++ "):
			cur = ""
		case strings.HasPrefix(line, "@@") && cur != "":
			m := hunkRE.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			start, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			count := 1
			if m[2] != "" {
				parsed, perr := strconv.Atoi(m[2])
				if perr != nil {
					continue
				}
				count = parsed
			}
			set := added[cur]
			if set == nil {
				set = map[int]bool{}
				added[cur] = set
			}
			for i := 0; i < count; i++ {
				set[start+i] = true
			}
		}
	}
	return added
}

func git(root, index string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if index != "" {
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
	}
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}
