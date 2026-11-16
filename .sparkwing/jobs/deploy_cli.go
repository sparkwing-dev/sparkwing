package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func deployCLICache(digest string) string {
	return filepath.Join(os.TempDir(), deployDirPrefix+"cli-"+digest, "sparkwing")
}

// safety: the gate output authenticates the digest, not the shared temporary directory.
func (d *deployer) productionCLI(ctx context.Context, commit string) error {
	if !d.dry && d.gate.Images.Commit == commit && d.gate.CLIDigest != "" && d.gate.CLIPlatform == runtime.GOOS+"/"+runtime.GOARCH {
		digest, err := hex.DecodeString(d.gate.CLIDigest)
		if err == nil && len(digest) == sha256.Size {
			data, err := os.ReadFile(deployCLICache(d.gate.CLIDigest))
			if err == nil && fmt.Sprintf("%x", sha256.Sum256(data)) == d.gate.CLIDigest {
				return d.act(ctx, "reuse the staging-verified candidate CLI for "+commit, func() error {
					if err := os.MkdirAll(filepath.Join(d.tmp, "cli-home"), 0o700); err != nil {
						return err
					}
					// #nosec G703 -- The deployment owns this temporary directory and cliBin selects its fixed CLI path.
					return os.WriteFile(d.cliBin(), data, 0o700)
				})
			}
		}
	}
	return d.buildCLI(ctx)
}

func (d *deployer) cacheCLI(ctx context.Context) string {
	if d.dry {
		return ""
	}
	data, err := os.ReadFile(d.cliBin())
	if err != nil {
		d.tools.say(ctx, "candidate CLI handoff unavailable; production will rebuild")
		return ""
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	path := deployCLICache(digest)
	err = os.MkdirAll(filepath.Dir(path), 0o700)
	if err == nil {
		var file *os.File
		file, err = os.CreateTemp(filepath.Dir(path), "candidate-")
		if err == nil {
			defer func() {
				if err := os.Remove(file.Name()); err != nil && !os.IsNotExist(err) {
					d.tools.say(ctx, "candidate CLI temporary file cleanup: "+err.Error())
				}
			}()
			_, err = file.Write(data)
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
			if err == nil {
				err = os.Rename(file.Name(), path)
			}
		}
	}
	if err != nil {
		d.tools.say(ctx, "candidate CLI handoff unavailable; production will rebuild")
		return ""
	}
	return digest
}
