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
		return renderPartialDoctor(os.Stdout, report, format, fmt.Errorf("doctor: %w", err))
	}
	return renderDoctor(os.Stdout, report, format)
}

func renderPartialDoctor(w io.Writer, report doctorReport, format string, diagnoseErr error) error {
	if len(report.PermissionRepairs) == 0 && !report.PermissionAuditUnverified {
		return diagnoseErr
	}
	return errors.Join(renderDoctor(w, report, format), diagnoseErr)
}

func diagnose(ctx context.Context, p paths.Paths, home string, dryRun bool) (doctorReport, error) {
	report, err := opsview.Diagnose(ctx, p, home, installedVersion(), dryRun)
	if err != nil {
		return report, err
	}
	report.ShadowedHooks = shadowedHooks(runGit)
	surveyed, err := surveyFleet(runGit)
	if err != nil {
		report.GatesSurveyError = err.Error()
		return report, nil
	}
	report.GatesSurveyed = len(surveyed)
	report.UngatedRepos = githooks.Ungated(surveyed)
	return report, nil
}

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
