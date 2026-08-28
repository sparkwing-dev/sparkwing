package orchestrator

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func ResolveDevEnvURL(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
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
		f, err := os.Open(filepath.Join(paths.Root, "dev.env"))
		if err != nil {
			return
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
			devEnvMap[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	})
	return devEnvMap
}
