package main

import (
	"errors"
	"fmt"
	"io"
	"runtime/debug"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Version is injected via -ldflags="-X main.Version=vX.Y.Z" by the
// release image build (build/Dockerfile.binary). Empty on a local
// `go build`, where the build-info VCS stamp supplies the commit
// instead.
var Version string

// provenance is the controller's self-identifying build metadata. The
// runs-store schema version is the load-bearing field: the controller
// refuses to open a state database newer than the schema it embeds, so
// printing the embedded schema at startup turns a skew into a one-line
// diagnosis instead of an opaque restart loop.
type provenance struct {
	Version  string
	Commit   string
	Modified bool
	Schema   int
}

// readProvenance gathers the running controller's build metadata. The
// version comes from the release ldflag when set; the commit and dirty
// flag come from the Go build-info VCS stamp, which is absent on builds
// made outside a git tree (some container builds), leaving Commit empty.
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

// line renders the one-line startup banner in a stable, greppable
// shape: "version <v>, runs-store schema <n>, commit <sha>".
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

// emitStartupProvenance writes the build banner to w. Called once at
// startup, before the controller touches the state database, so the
// embedded schema version is on the record even when the open fails.
func emitStartupProvenance(w io.Writer) {
	fmt.Fprintln(w, "sparkwing-controller:", readProvenance().line())
}

// skewRefusalMessage is the controller's actionable response to a state
// database recorded at a newer schema than this build understands. It
// names both versions and the operator's remedy.
func skewRefusalMessage(e *store.SkewError) string {
	return fmt.Sprintf(
		"runs-store schema skew -- the state database is at schema version %d, "+
			"but this controller understands schema %d. The controller will not "+
			"open a newer store (doing so risks corrupting records it cannot model). "+
			"Roll the controller forward to a build that understands schema %d, or "+
			"restore the database to a schema-%d snapshot.",
		e.DBVersion, e.BinaryVersion, e.DBVersion, e.BinaryVersion,
	)
}

// mapStoreOpenError turns a store.Open failure into the error the
// controller exits with. A schema skew is refused with the legible,
// actionable message above; any other failure keeps the generic
// open-state-db framing.
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
