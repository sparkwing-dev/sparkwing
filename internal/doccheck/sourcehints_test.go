package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/gitenv/testgit"
)

//sparkwing:critical
func TestRepositorySourceHintsUseCurrentGrammar(t *testing.T) {
	if !checkSourceHints(filepath.Join("..", "..")) {
		t.Fatal("Go source prints commands outside the current grammar")
	}
}

func TestCheckSourceHintsRejectsStaleHints(t *testing.T) {
	for _, literal := range []string{
		`"  upgrade:  sparkwing update --cli"`,
		`("sparkwing serve start")`,
		`"sparkwing serve " + "start"`,
		`"sparkwing serve start\n" + suffix`,
		`prefix + "\nsparkwing serve start\n" + suffix`,
		"\"run `sparkwing cluster runners add`\"",
		"\"run `sparkwing unknown-root repair`\"",
		`"run sparkwing cluster object-store reset-breaker --profile NAME"`,
		`"run sparkwing unknown-root repair"`,
		`"Try sparkwing unknown-root repair"`,
		"`Recover the dashboard:\n\n  sparkwing serve start\n\nThen retry.`",
		"`Example:\n  sparkwing retired-root recover\n`",
		"`Example:\n  $ sparkwing retired-root recover\n`",
		"`sparkwing unknown-root repair`",
		`"install the matching CLI: sparkwing update --cli --version %s"`,
		"\"no repos registered; run `sparkwing pipeline add <dir>` first\"",
		`"(sparkwing secrets set --name KEY --file <path>)"`,
	} {
		root := t.TempDir()
		writeSource(t, root, "cmd/tool/hint.go", "package main\n\nvar hint = "+literal+"\n")
		trackSources(t, root)
		if checkSourceHints(root) {
			t.Fatalf("stale hint accepted: %s", literal)
		}
	}
}

func TestCheckSourceHintsAcceptsCurrentHintsAndProse(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "cmd/tool/hint.go", "package main\n\nvar hints = []string{\n"+
		"\t\"run sparkwing doctor, then sparkwing update --version %s to retry\",\n"+
		"\t\"add one with `sparkwing repos add`\",\n"+
		"\t\"the daemon creates it; sparkwing repos and their SDK pins\",\n"+
		"\t\"sparkwing-dev/sparkwing update --cli\",\n"+
		"}\n")
	writeSource(t, root, "cmd/tool/notice.go", "package main\n\nvar notice = \"sparkwing changed %s -> %s: see `sparkwing docs read --topic changelog`\"\n")
	writeSource(t, root, "internal/data/values.go", `package data
var key = []byte("sparkwing signing domain v1")
var message = "sparkwing will build with " + "go1.26."
var version string
var fragment = "sparkwing will build with " + version
var current = "sparkwing dashboard " + "open"
func data() { run("commit", "-m", "sparkwing sample commit"); strings.NewReader("sparkwing snapshot\n") }
`)
	writeSource(t, root, "cmd/tool/hint_test.go", "package main\n\nvar old = \"sparkwing update --cli\"\n")
	writeSource(t, root, "internal/x/testdata/hint.go", "package x\n\nvar old = \"sparkwing update --cli\"\n")
	trackSources(t, root)
	if !checkSourceHints(root) {
		t.Fatal("current hints or prose rejected")
	}
}

func writeSource(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func FuzzConcatenatedRecoveryCommandsUseCurrentGrammar(f *testing.F) {
	f.Add(uint8(0))
	f.Add(uint8(10))
	f.Add(uint8(20))
	f.Fuzz(func(t *testing.T, split uint8) {
		for _, command := range []string{"sparkwing serve start\n", "sparkwing dashboard open\n", "sparkwing serve start", "sparkwing dashboard open"} {
			root := t.TempDir()
			point := int(split) % len(command)
			expression := "(" + strconv.Quote(command[:point]) + " + " + strconv.Quote(command[point:]) + ")"
			if strings.HasSuffix(command, "\n") {
				expression += " + suffix"
			}
			writeSource(t, root, "cmd/tool/hint.go", "package main\nvar suffix string\nvar hint = "+expression+"\n")
			want := strings.HasPrefix(command, "sparkwing dashboard")
			trackSources(t, root)
			if got := checkSourceHints(root); got != want {
				t.Fatalf("grammar verdict=%t want=%t: %s", got, want, expression)
			}
		}
	})
}

func FuzzNamedConstantRecoveryCommandsUseCurrentGrammar(f *testing.F) {
	f.Add(uint8(46), uint8(4))
	for form := range uint8(7) {
		f.Add(uint8(10), form)
	}
	f.Fuzz(func(t *testing.T, split, form uint8) {
		for _, command := range []string{"sparkwing serve start", "sparkwing dashboard open"} {
			root := t.TempDir()
			point := int(split) % len(command)
			declarations := "const head = " + strconv.Quote(command[:point]) + "\nconst tail = " + strconv.Quote(command[point:]) + "\n"
			expression := "head + tail"
			prefix, suffix := "package main\nvar suffix string\n", ""
			switch form % 7 {
			case 1:
				expression = "head + tail + string(rune(10)) + suffix"
			case 2:
				declarations += "const alias = head + (tail)\n"
				expression = "alias"
			case 3:
				declarations += "const (alias = head + tail; repeated)\n"
				expression = "repeated"
			case 4:
				writeSource(t, root, "cmd/tool/constants.go", "package main\n"+declarations)
				declarations = ""
			case 5:
				prefix += "const head = \"unrelated\"\nfunc printHint() {\n"
				suffix = "}\n"
			case 6:
				prefix = "package main\nimport \"fmt\"\nfunc printHint() {\n"
				expression = "fmt.Sprint(" + expression + ")"
				suffix = "}\n"
			}
			writeSource(t, root, "cmd/tool/hint.go", prefix+declarations+"var hint = "+expression+"\n"+suffix)
			want := strings.HasPrefix(command, "sparkwing dashboard")
			trackSources(t, root)
			if got := checkSourceHints(root); got != want {
				t.Fatalf("named constant grammar verdict=%t want=%t: form=%d expression=%s", got, want, form%7, expression)
			}
		}
	})
}

func trackSources(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "."}} {
		if output, err := testgit.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
}

func TestSourceChecksJudgeOnlyTrackedFiles(t *testing.T) {
	for _, tc := range []struct {
		name, violation string
		check           func(string) bool
	}{
		{"comments", "package fixture\n// pipelines.yaml is retired.\n", func(root string) bool { return checkProductionComments(root) == nil }},
		{"hints", "package fixture\nvar hint = \"run sparkwing unknown-root repair\"\n", checkSourceHints},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tracked := "pkg/cachecontrol/with space.go"
			writeSource(t, root, ".gitignore", ".Codex-scratch/\n")
			writeSource(t, root, tracked, "package fixture\n")
			trackSources(t, root)
			writeSource(t, root, "pkg/cachecontrol/.Codex-scratch/bad.go", "malformed Go\n")
			writeSource(t, root, "pkg/cachecontrol/untracked.go", "malformed Go\n")
			if output, err := testgit.Command("git", "-C", root, "check-ignore", "pkg/cachecontrol/.Codex-scratch/bad.go").CombinedOutput(); err != nil {
				t.Fatalf("scratch must be ignored: %v\n%s", err, output)
			}
			if !tc.check(root) {
				t.Fatal("untracked malformed sources failed the check")
			}
			writeSource(t, root, tracked, tc.violation)
			if tc.check(root) {
				t.Fatal("tracked violation escaped the check")
			}
		})
	}
}
