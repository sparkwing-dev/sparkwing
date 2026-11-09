package bincache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func effectiveBuildInputs(ctx context.Context, dir string, env []string) ([]byte, []byte, error) {
	if env == nil {
		env = os.Environ()
	}
	env = pipelineBuildEnvironment(env)
	tool, err := gotoolchain.Executable(env)
	if err != nil {
		// safety: no compiler can publish under this identity; a compiler's arrival changes the key.
		return nil, nil, nil //nolint:nilerr // Missing Go has a distinct identity and cannot publish a binary.
	}
	env, err = gotoolchain.BuildEnv(ctx, dir, env, EffectiveOverlay(dir, env))
	if err != nil {
		return nil, nil, err
	}
	work, present := goWorkInScope(dir, env)
	if present && !goWorkCovers(work, dir) {
		env = withGoworkOff(env)
	}
	output := func(args ...string) ([]byte, error) {
		cmd := procenv.ControlCommand(env, tool, args...)
		cmd.Dir = dir
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := gotoolchain.Run(ctx, cmd); err != nil {
			return nil, fmt.Errorf("read Go build inputs: %w: %s", err, &stderr)
		}
		return stdout.Bytes(), nil
	}
	settings, err := output("env", "-json")
	if err != nil {
		return nil, nil, err
	}
	var effective map[string]string
	if err := json.Unmarshal(settings, &effective); err != nil {
		return nil, nil, fmt.Errorf("read effective Go environment: %w", err)
	}
	resolved := append([]string(nil), env...)
	for _, name := range buildEnvironmentNames {
		// perf: -trimpath and content digests make the default module-cache location irrelevant.
		if name == "GOPATH" {
			continue
		}
		resolved = append(resolved, name+"="+effective[name])
	}
	args := append([]string{"list", "-e", "-deps", "-json"}, pipelineBuildFlags...)
	if overlay := EffectiveOverlay(dir, env); overlay != "" {
		args = append(args, "-modfile="+overlay)
	}
	args = append(args, ".")
	compiled, err := compiledInputDigest(func() ([]byte, error) { return output(args...) }, dir, effective["GOFLAGS"], effective["GOWORK"])
	return buildEnvironmentKey(resolved), compiled, err
}

func compiledInputDigest(list func() ([]byte, error), dir, flags, workspace string) ([]byte, error) {
	var previous []byte
	for range 2 {
		raw, err := list()
		if err != nil {
			return nil, err
		}
		var digest bytes.Buffer
		replacements, selected, err := selectedBuildFiles(dir, flags)
		if err != nil {
			return nil, err
		}
		if replacement := replacements[workspace]; replacement != "" {
			selected = append(selected, replacement)
		}
		if _, err := hashFilesInto(&digest, dir, selected); err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		for {
			var pkg struct {
				Dir, ImportPath                                                                                                     string
				GoFiles, CgoFiles, CFiles, CXXFiles, MFiles, HFiles, FFiles, SFiles, SwigFiles, SwigCXXFiles, SysoFiles, EmbedFiles []string
				Error                                                                                                               json.RawMessage
				DepsErrors                                                                                                          json.RawMessage
				Module                                                                                                              *struct {
					GoMod   string
					Replace *struct{ GoMod string }
				}
			}
			if err := decoder.Decode(&pkg); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return nil, err
			}
			fmt.Fprintf(&digest, "package:%s\n%s\n%s\n", pkg.ImportPath, pkg.Error, pkg.DepsErrors)
			var files []string
			var metadata []string
			if pkg.Module != nil {
				path := pkg.Module.GoMod
				if pkg.Module.Replace != nil {
					path = pkg.Module.Replace.GoMod
				}
				if path != "" {
					if !filepath.IsAbs(path) {
						path, err = filepath.Abs(filepath.Join(dir, path))
						if err != nil {
							return nil, err
						}
					}
					metadata = append(metadata, path)
				}
			}
			for _, group := range [][]string{pkg.GoFiles, pkg.CgoFiles, pkg.CFiles, pkg.CXXFiles, pkg.MFiles, pkg.HFiles, pkg.FFiles, pkg.SFiles, pkg.SwigFiles, pkg.SwigCXXFiles, pkg.SysoFiles, pkg.EmbedFiles, metadata} {
				for _, name := range group {
					path := name
					if !filepath.IsAbs(path) {
						path = filepath.Join(pkg.Dir, path)
					}
					if replacement, ok := replacements[path]; ok {
						path = replacement
					}
					if path != "" {
						files = append(files, path)
					}
				}
			}
			sort.Strings(files)
			if _, err := hashFilesInto(&digest, pkg.Dir, files); err != nil {
				return nil, err
			}
		}
		if previous != nil && !bytes.Equal(previous, digest.Bytes()) {
			return nil, ErrUnstableInputs
		}
		previous = append([]byte{}, digest.Bytes()...)
	}
	return previous, nil
}

func selectedBuildFiles(dir, flags string) (map[string]string, []string, error) {
	values := map[string]string{"pgo": "auto"}
	// safety: GOFLAGS quotes entire words and does not unescape their contents.
	for flags != "" {
		flags = strings.TrimLeft(flags, " \t\r\n")
		if flags == "" {
			break
		}
		var word string
		if flags[0] == '\'' || flags[0] == '"' {
			end := strings.IndexByte(flags[1:], flags[0])
			if end < 0 {
				return nil, nil, errors.New("unterminated GOFLAGS quote")
			}
			word, flags = flags[1:end+1], flags[end+2:]
		} else {
			end := strings.IndexAny(flags, " \t\r\n")
			if end < 0 {
				end = len(flags)
			}
			word, flags = flags[:end], flags[end:]
		}
		name, value, _ := strings.Cut(strings.TrimLeft(word, "-"), "=")
		values[name] = value
	}
	absolute := func(path string) string {
		if filepath.IsAbs(path) {
			return filepath.Clean(path)
		}
		return filepath.Join(dir, path)
	}
	var files []string
	replacements := map[string]string{}
	if path := values["overlay"]; path != "" {
		path = absolute(path)
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		var overlay struct{ Replace map[string]string }
		if err := json.Unmarshal(body, &overlay); err != nil {
			return nil, nil, err
		}
		files = append(files, path)
		for original, replacement := range overlay.Replace {
			if replacement != "" {
				replacement = absolute(replacement)
			}
			replacements[absolute(original)] = replacement
		}
	}
	profile := values["pgo"]
	if profile == "auto" {
		profile = filepath.Join(dir, "default.pgo")
		if _, err := os.Stat(profile); errors.Is(err, os.ErrNotExist) {
			profile = "off"
		} else if err != nil {
			return nil, nil, err
		}
	}
	if profile != "off" {
		files = append(files, absolute(profile))
	}
	sort.Strings(files)
	return replacements, files, nil
}
