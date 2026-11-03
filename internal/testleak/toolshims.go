package testleak

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// safety: asdf and mise shims find the real tool through files under HOME (the
// data dir, ~/.tool-versions, the mise config dir). The sandbox moves HOME, so
// a test that runs go or helm through a shim would get "No version is set".
// These pins name the real locations and versions while HOME stays private;
// a variable the developer already set is left alone.
func toolResolutionEnv(realHome, cwd string, getenv func(string) string) map[string]string {
	env := map[string]string{}
	if realHome == "" {
		return env
	}
	pinDir := func(name, dir string) {
		if getenv(name) != "" {
			return
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			env[name] = dir
		}
	}
	asdfData := getenv("ASDF_DATA_DIR")
	if asdfData == "" {
		asdfData = filepath.Join(realHome, ".asdf")
		pinDir("ASDF_DATA_DIR", asdfData)
	}
	if _, err := os.Stat(asdfData); err == nil {
		if rc := filepath.Join(realHome, ".asdfrc"); getenv("ASDF_CONFIG_FILE") == "" && fileExists(rc) {
			env["ASDF_CONFIG_FILE"] = rc
		}
		name := getenv("ASDF_DEFAULT_TOOL_VERSIONS_FILENAME")
		if name == "" {
			name = ".tool-versions"
		}
		for tool, version := range toolVersions(cwd, realHome, name) {
			key := "ASDF_" + strings.ToUpper(strings.ReplaceAll(tool, "-", "_")) + "_VERSION"
			if getenv(key) == "" {
				env[key] = version
			}
		}
	}
	configHome := getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(realHome, ".config")
	}
	pinDir("MISE_CONFIG_DIR", filepath.Join(configHome, "mise"))
	dataHome := getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(realHome, ".local", "share")
	}
	pinDir("MISE_DATA_DIR", filepath.Join(dataHome, "mise"))
	return env
}

// safety: asdf takes each tool's version from the nearest file that names it,
// walking up from the working directory and ending at the home directory, so
// the pins resolve the version the shim would have chosen before HOME moved.
func toolVersions(cwd, home, name string) map[string]string {
	versions := map[string]string{}
	var files []string
	for dir := cwd; dir != ""; {
		files = append(files, filepath.Join(dir, name))
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	files = append(files, filepath.Join(home, name))
	for _, file := range files {
		for tool, version := range readToolVersions(file) {
			if _, seen := versions[tool]; !seen {
				versions[tool] = version
			}
		}
	}
	return versions
}

func readToolVersions(file string) map[string]string {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			out[fields[0]] = fields[1]
		}
	}
	return out
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// safety: the go command a test spawns must be the toolchain that built the
// suite, not whatever a shim resolves under the sandbox HOME.
func goRootFirstOnPath(goroot, path string) string {
	if goroot == "" {
		return ""
	}
	bin := filepath.Join(goroot, "bin")
	if info, err := os.Stat(bin); err != nil || !info.IsDir() {
		return ""
	}
	if path == "" {
		return bin
	}
	return bin + string(os.PathListSeparator) + path
}
