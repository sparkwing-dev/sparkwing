// `sparkwing doctor` -- the one safe repair verb. It finds local state
// that is safe to remove because the process behind it is provably gone,
// repairs it, and reports everything it saw and did. It never kills a
// process, never touches the admission daemon's live state, and never
// touches cluster-scoped (global) rows, so it is safe to run at any time
// and a healthy machine gets a clean bill. The diagnosis and rendering
// are shared with the headless pipeline binary through internal/opsview.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

// doctorReport is the shared shape of a doctor sweep's result.
type doctorReport = opsview.DoctorReport

func runDoctor(args []string) error {
	fs := flag.NewFlagSet(cmdDoctor.Path, flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "report what would be repaired without changing anything")
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	home := fs.String("home", "", "sparkwing home to inspect (default: $SPARKWING_HOME or ~/.sparkwing)")
	if err := parseAndCheck(cmdDoctor, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdDoctor.Path)
	if err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("doctor: unexpected positional %q (doctor takes flags only)", fs.Arg(0))
	}

	p, err := homePaths(*home)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	report, err := diagnose(ctx, p, *home, *dryRun)
	if err != nil {
		return fmt.Errorf("doctor: %w", err)
	}
	return renderDoctor(os.Stdout, report, format)
}

func diagnose(ctx context.Context, p paths.Paths, home string, dryRun bool) (doctorReport, error) {
	report, err := opsview.Diagnose(ctx, p, home, installedVersion(), dryRun)
	if err != nil {
		return report, err
	}
	report.ShadowedHooks = shadowedHooks(runGit)
	return report, nil
}

// shadowedHooks reports the sparkwing hooks installed in the checkout doctor
// was run from that git will never execute, because core.hooksPath points
// elsewhere. Nothing is reported outside a sparkwing project, or when git
// reads the directory the hooks live in.
func shadowedHooks(git githooks.Git) *githooks.Shadow {
	sparkwingDir, err := findSparkwingDir()
	if err != nil {
		return nil
	}
	shadow, err := githooks.Detect(git, filepath.Dir(sparkwingDir))
	if err != nil {
		return nil
	}
	return shadow
}

func renderDoctor(w io.Writer, r doctorReport, format string) error {
	return opsview.RenderDoctor(w, r, format, legacyWarningLine(len(r.LiveLegacyHolders)))
}
