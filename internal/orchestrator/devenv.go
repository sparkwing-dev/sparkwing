package orchestrator

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// DevEnvDisableEnv turns off the dev.env fallback while it holds any value. A
// process that sets it resolves a service URL from its own environment and
// nowhere else, which is how a test suite stays off the services the
// operator's dev.env names.
const DevEnvDisableEnv = "SPARKWING_DEV_ENV_DISABLE"

func ResolveDevEnvURL(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if os.Getenv(DevEnvDisableEnv) != "" {
		return ""
	}
	return devEnvFile()[key]
}

var (
	devEnvOnce sync.Once
	devEnvMap  map[string]string
)

func devEnvFile() map[string]string {
	devEnvOnce.Do(func() {
		devEnvMap = map[string]string{}
		paths, err := DefaultPaths()
		if err != nil {
			return
		}
		devEnvMap = readDevEnv(paths.Root)
	})
	return devEnvMap
}

func readDevEnv(root string) map[string]string {
	values := map[string]string{}
	f, err := os.Open(filepath.Join(root, "dev.env"))
	if err != nil {
		return values
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return values
}
