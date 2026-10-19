// Command qagoproxy writes one module version in the GOPROXY layout, built from
// a git revision the way the public proxy builds it, so QA pipelines can fetch
// an SDK version that has no public tag.
//
//	go run ./bin/qagoproxy --repo . --revision <sha> --version v0.68.0-qa.3 --out goproxy
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/zip"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "qagoproxy:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("qagoproxy", flag.ContinueOnError)
	repo := flags.String("repo", "", "git checkout whose root is the module")
	revision := flags.String("revision", "", "commit to package")
	version := flags.String("version", "", "module version to publish")
	out := flags.String("out", "", "GOPROXY root to write into")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repo == "" || *revision == "" || *version == "" || *out == "" {
		return errors.New("--repo, --revision, --version and --out are required")
	}
	show := exec.Command("git", "-C", *repo, "show", "-s", "--format=%cI", *revision)
	stamp, err := show.Output()
	if err != nil {
		return fmt.Errorf("read commit time: %w", err)
	}
	committed, err := time.Parse(time.RFC3339, strings.TrimSpace(string(stamp)))
	if err != nil {
		return err
	}
	read := exec.Command("git", "-C", *repo, "show", *revision+":go.mod")
	gomod, err := read.Output()
	if err != nil {
		return fmt.Errorf("read go.mod: %w", err)
	}
	path := modfile.ModulePath(gomod)
	m := module.Version{Path: path, Version: *version}
	if err := module.Check(m.Path, m.Version); err != nil {
		return err
	}
	escaped, err := module.EscapePath(path)
	if err != nil {
		return err
	}
	dir := filepath.Join(*out, filepath.FromSlash(escaped), "@v")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	archive, err := os.Create(filepath.Join(dir, *version+".zip"))
	if err != nil {
		return err
	}
	if err := errors.Join(zip.CreateFromVCS(archive, m, *repo, *revision, ""), archive.Close()); err != nil {
		return fmt.Errorf("create module zip: %w", err)
	}
	info, err := json.Marshal(struct {
		Version string
		Time    time.Time
	}{*version, committed.UTC()})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, *version+".info"), info, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, *version+".mod"), gomod, 0o644)
}
