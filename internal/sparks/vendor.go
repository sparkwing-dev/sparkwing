package sparks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
)

const VendoredDirName = "sparks"

type VendorResult struct {
	ModulePath string

	Version string

	Dest string

	RelReplace string
}

func Vendor(ctx context.Context, sparkwingDir, modulePath string) (*VendorResult, error) {
	if sparkwingDir == "" {
		return nil, errors.New("sparks: sparkwingDir must not be empty")
	}
	modulePath = strings.TrimSpace(modulePath)
	if modulePath == "" {
		return nil, errors.New("sparks: module path must not be empty")
	}
	goModPath := filepath.Join(sparkwingDir, goModFilename)
	rawGoMod, err := os.ReadFile(goModPath)
	if err != nil {
		return nil, fmt.Errorf("sparks: read %s: %w", goModPath, err)
	}

	base := path.Base(modulePath)
	dest := filepath.Join(sparkwingDir, VendoredDirName, base)
	if _, err := os.Stat(dest); err == nil {
		return nil, fmt.Errorf("sparks: %s already exists; refusing to overwrite (delete it first to re-vendor)", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("sparks: stat %s: %w", dest, err)
	}

	version := requiredVersion(rawGoMod, goModPath, modulePath)
	if version == "" {
		version = "latest"
	}

	srcDir, resolvedVer, err := downloadModule(ctx, sparkwingDir, modulePath, version)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, fmt.Errorf("sparks: create %s: %w", filepath.Dir(dest), err)
	}
	if err := copyModuleTree(srcDir, dest); err != nil {
		return nil, err
	}
	if err := makeTreeWritable(dest); err != nil {
		return nil, err
	}

	relReplace := "./" + path.Join(VendoredDirName, base)
	if err := addReplaceDirective(goModPath, rawGoMod, modulePath, relReplace); err != nil {
		return nil, err
	}
	if err := goModTidy(ctx, sparkwingDir); err != nil {
		return nil, err
	}

	return &VendorResult{
		ModulePath: modulePath,
		Version:    resolvedVer,
		Dest:       dest,
		RelReplace: relReplace,
	}, nil
}

func requiredVersion(rawGoMod []byte, goModPath, modulePath string) string {
	f, err := modfile.Parse(goModPath, rawGoMod, nil)
	if err != nil {
		return ""
	}
	for _, req := range f.Require {
		if req.Mod.Path == modulePath {
			return req.Mod.Version
		}
	}
	return ""
}

type modDownloadJSON struct {
	Path    string
	Version string
	Dir     string
	Error   string
}

func downloadModule(ctx context.Context, sparkwingDir, modulePath, version string) (dir, resolved string, err error) {
	cmd := exec.CommandContext(ctx, goBin(), "mod", "download", "-json", modulePath+"@"+version)
	cmd.Dir = sparkwingDir
	cmd.Env = os.Environ()
	out, runErr := cmd.Output()
	if len(out) == 0 && runErr != nil {
		return "", "", fmt.Errorf("sparks: go mod download %s@%s: %w", modulePath, version, runErr)
	}
	var info modDownloadJSON
	if jerr := json.Unmarshal(out, &info); jerr != nil {
		return "", "", fmt.Errorf("sparks: decode go mod download output: %w", jerr)
	}
	if info.Error != "" {
		return "", "", fmt.Errorf("sparks: go mod download %s@%s: %s", modulePath, version, info.Error)
	}
	if info.Dir == "" {
		return "", "", fmt.Errorf("sparks: go mod download %s@%s returned no directory", modulePath, version)
	}
	return info.Dir, info.Version, nil
}

func copyModuleTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		if rel == "vendor" && d.IsDir() {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(p, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("sparks: open %s: %w", src, err)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("sparks: create %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("sparks: copy %s: %w", dst, err)
	}
	return out.Close()
}

func makeTreeWritable(root string) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		return os.Chmod(p, info.Mode()|0o200)
	})
}

func addReplaceDirective(goModPath string, rawGoMod []byte, modulePath, relReplace string) error {
	f, err := modfile.Parse(goModPath, rawGoMod, nil)
	if err != nil {
		return fmt.Errorf("sparks: parse %s: %w", goModPath, err)
	}
	if err := f.AddReplace(modulePath, "", relReplace, ""); err != nil {
		return fmt.Errorf("sparks: add replace for %s: %w", modulePath, err)
	}
	f.Cleanup()
	formatted, err := f.Format()
	if err != nil {
		return fmt.Errorf("sparks: format %s: %w", goModPath, err)
	}
	if err := os.WriteFile(goModPath, formatted, 0o644); err != nil {
		return fmt.Errorf("sparks: write %s: %w", goModPath, err)
	}
	return nil
}

func goModTidy(ctx context.Context, sparkwingDir string) error {
	cmd := exec.CommandContext(ctx, goBin(), "mod", "tidy")
	cmd.Dir = sparkwingDir
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sparks: go mod tidy: %w: %s", err, string(out))
	}
	return nil
}
