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

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func effectiveBuildInputs(ctx context.Context, dir string, env []string) ([]byte, []byte, error) {
	if env == nil {
		env = os.Environ()
	}
	env = pipelineBuildEnvironment(env)
	tool, _ := gotoolchain.Executable(env)
	if tool == "" {
		// safety: no compiler can publish under this identity; a compiler's arrival changes the key.
		return nil, nil, nil
	}
	env, err := gotoolchain.BuildEnv(ctx, dir, env, EffectiveOverlay(dir, env))
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
	compiled, err := compiledInputDigest(func() ([]byte, error) { return output(args...) })
	return buildEnvironmentKey(resolved), compiled, err
}

func compiledInputDigest(list func() ([]byte, error)) ([]byte, error) {
	var previous []byte
	for range 2 {
		raw, err := list()
		if err != nil {
			return nil, err
		}
		var digest bytes.Buffer
		decoder := json.NewDecoder(bytes.NewReader(raw))
		for {
			var pkg struct {
				Dir, ImportPath                                                                                                     string
				GoFiles, CgoFiles, CFiles, CXXFiles, MFiles, HFiles, FFiles, SFiles, SwigFiles, SwigCXXFiles, SysoFiles, EmbedFiles []string
				Error                                                                                                               json.RawMessage
				DepsErrors                                                                                                          json.RawMessage
			}
			if err := decoder.Decode(&pkg); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return nil, err
			}
			fmt.Fprintf(&digest, "package:%s\n%s\n%s\n", pkg.ImportPath, pkg.Error, pkg.DepsErrors)
			var files []string
			for _, group := range [][]string{pkg.GoFiles, pkg.CgoFiles, pkg.CFiles, pkg.CXXFiles, pkg.MFiles, pkg.HFiles, pkg.FFiles, pkg.SFiles, pkg.SwigFiles, pkg.SwigCXXFiles, pkg.SysoFiles, pkg.EmbedFiles} {
				for _, name := range group {
					files = append(files, filepath.Join(pkg.Dir, name))
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
