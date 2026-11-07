package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestBashCompletionWithoutMapfile(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	for _, tc := range []struct{ name, words, index, want string }{
		{"flag", "sparkwing --p", "1", "--profile"},
		{"verb", "sparkwing r", "1", "run"},
		{"leaf flags", "sparkwing leaf ''", "2", "--profile"},
		{"no rows", "sparkwing empty ''", "2", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := "set -u\nif type -t mapfile >/dev/null; then enable -n mapfile; fi\n" + renderBash() + `
sparkwing() {
 case "$1" in
 __complete)
  case "$3" in
  verbs) if [[ "${5-}" != leaf && "${5-}" != empty ]]; then printf '%s\n' run; fi ;;
  flags) if [[ "${5-}" != empty ]]; then printf '%s\n' --profile; fi ;;
  esac ;;
 esac
}
` + "COMP_WORDS=(" + tc.words + ")\nCOMP_CWORD=" + tc.index + "\n_sparkwing_complete\nprintf '%s\n' ${COMPREPLY[@]+\"${COMPREPLY[@]}\"}\n"
			out, err := exec.Command(bash, "--noprofile", "--norc", "-c", script).CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != tc.want {
				t.Fatalf("completion=%q err=%v want=%q", out, err, tc.want)
			}
		})
	}
}

func TestPipelineCompletionCarriesNameAndDescription(t *testing.T) {
	projectAt(t, "pipelines:\n  - name: demo\n    entrypoint: Demo\n")
	out := captureStdout(t, func() {
		if err := runInternalCompletePipelines(os.Stdout, nil); err != nil {
			t.Fatal(err)
		}
	})
	columns := strings.Split(strings.TrimSuffix(out, "\n"), "\t")
	if len(columns) != 2 || columns[0] != "demo" || columns[1] != "manual" {
		t.Fatalf("completion columns=%q", columns)
	}
}

func TestZshPipelineCompletionUsesCurrentRows(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	script := "compdef() { :; }\n" + renderZsh() + `
sparkwing() { printf 'demo\tmanual\nlong-pipeline-name\tbuild checks\n'; }
compadd() { printf '%s\n' "${names[@]}" "${displays[@]}"; }
_sparkwing_complete_pipelines
`
	out, err := exec.Command(zsh, "-f", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("zsh completion: %s: %v", out, err)
	}
	rows := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(rows) != 4 || rows[0] != "demo" || rows[1] != "long-pipeline-name" ||
		!strings.Contains(rows[2], "[pipeline]  manual") || !strings.Contains(rows[3], "[pipeline]  build checks") {
		t.Fatalf("completion rows = %q", rows)
	}
}
