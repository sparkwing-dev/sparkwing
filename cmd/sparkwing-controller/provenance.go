package main

import (
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	fmt.Fprintln(w, "sparkwing-controller:", readProvenance().line())
}

func skewRefusalMessage(e *store.SkewError) string {
	if len(e.Requirements) > 0 {
		return fmt.Sprintf(
			"runs-store requirement skew -- the state database uses %s, which this "+
				"controller does not understand (database schema version %d, this "+
				"controller understands schema %d). The controller will not open a store "+
				"whose records it cannot model. Roll the controller forward to a build "+
				"that understands %s, or restore the database to a snapshot without it.",
			strings.Join(e.Requirements, ", "), e.DBVersion, e.BinaryVersion,
			strings.Join(e.Requirements, ", "),
		)
	}
	return fmt.Sprintf(
		"runs-store schema skew -- the state database is at schema version %d, "+
			"but this controller understands schema %d. The controller will not "+
			"open a newer store (doing so risks corrupting records it cannot model). "+
			"Roll the controller forward to a build that understands schema %d, or "+
			"restore the database to a schema-%d snapshot.",
		e.DBVersion, e.BinaryVersion, e.DBVersion, e.BinaryVersion,
	)
}

func mapStoreOpenError(err error) error {
	if err == nil {
		return nil
	}
	var skew *store.SkewError
	if errors.As(err, &skew) {
		return fmt.Errorf("refusing to start: %s", skewRefusalMessage(skew))
	}
	return fmt.Errorf("open state db: %w", err)
}
