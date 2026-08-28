package gitenv

import (
	"os"
	"path/filepath"
	"strings"
)

var bindingVars = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_PREFIX",
	"GIT_COMMON_DIR",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_NAMESPACE",
	"GIT_QUARANTINE_PATH",
}

const GateIndexVar = "SPARKWING_GATE_INDEX"

func Unbind() {
	if index := os.Getenv("GIT_INDEX_FILE"); index != "" {
		if abs, err := filepath.Abs(index); err == nil {
			index = abs
		}
		_ = os.Setenv(GateIndexVar, index)
	}
	for _, name := range bindingVars {
		_ = os.Unsetenv(name)
	}
}

func GateIndex() string {
	path := os.Getenv(GateIndexVar)
	if path == "" {
		return ""
	}
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

func ShellUnbind() string {
	return "if [ -n \"${GIT_INDEX_FILE:-}\" ]; then\n" +
		"\tcase \"$GIT_INDEX_FILE\" in\n" +
		"\t/*) " + GateIndexVar + "=\"$GIT_INDEX_FILE\" ;;\n" +
		"\t*) " + GateIndexVar + "=\"$(pwd)/$GIT_INDEX_FILE\" ;;\n" +
		"\tesac\n" +
		"\texport " + GateIndexVar + "\n" +
		"fi\n" +
		"unset " + strings.Join(bindingVars, " ") + "\n"
}
