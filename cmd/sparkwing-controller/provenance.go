package main

import (
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

var Version string

type provenance struct {
	Version  string
	Commit   string
	Modified bool
	Schema   int
}

func readProvenance() provenance {
	p := provenance{Version: Version, Schema: store.ExpectedSchemaVersion()}
	if info, ok := debug.ReadBuildInfo(); ok {
		if p.Version == "" {
			p.Version = info.Main.Version
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				p.Commit = s.Value
			case "vcs.modified":
				p.Modified = s.Value == "true"
			}
		}
	}
	return p
}

func (p provenance) line() string {
	version := p.Version
	if version == "" {
		version = "(unknown)"
	}
	commit := p.Commit
	switch {
	case commit == "":
		commit = "(unknown)"
	case p.Modified:
		commit += "+dirty"
	}
	return fmt.Sprintf("version %s, runs-store schema %d, commit %s", version, p.Schema, commit)
}

func emitStartupProvenance(w io.Writer) {
	slog.New(controller.CredentialSafeLogHandler(slog.NewTextHandler(w, nil))).Info("sparkwing-controller: " + readProvenance().line())
}
