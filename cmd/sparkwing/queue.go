package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func runQueue(args []string) error {
	if len(args) > 0 && args[0] == "exec" {
		return runQueueExec(args[1:])
	}
	fs := flag.NewFlagSet(cmdQueue.Path, flag.ContinueOnError)
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	home := fs.String("home", "", "sparkwing home to inspect (default: $SPARKWING_HOME or ~/.sparkwing)")
	on := addProfileFlag(fs)
	if err := parseAndCheck(cmdQueue, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdQueue.Path)
	if err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("queue: unexpected positional %q (queue takes flags only)", fs.Arg(0))
	}

	if *on != "" {
		return runQueueProfile(*on, format)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	qs, err := wingdclient.Query(ctx, wingdclient.Options{Home: *home, Version: Version})
	legacy, _ := liveLegacyBoxSlots(*home)

	if err != nil {
		if errors.Is(err, wingdclient.ErrDaemonUnreachable) {
			if rerr := renderUnreachableDaemon(os.Stdout, format, err); rerr != nil {
				return rerr
			}
			warnLegacy(os.Stderr, len(legacy))
			return exitError(4, fmt.Errorf("queue: %w", err))
		}
		if errors.Is(err, wingdclient.ErrNoDaemon) {
			if rerr := renderNoDaemon(os.Stdout, format); rerr != nil {
				return rerr
			}
			warnLegacy(os.Stderr, len(legacy))
			return nil
		}
		return fmt.Errorf("queue: %w", err)
	}
	if rerr := renderLocalQueue(os.Stdout, qs, format); rerr != nil {
		return rerr
	}
	warnLegacy(os.Stderr, len(legacy))
	return nil
}

func runQueueProfile(profileName, format string) error {
	prof, err := resolveProfile(profileName)
	if err != nil {
		return err
	}
	if err := requireController(prof, "queue"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	qs, err := fetchControllerQueueState(ctx, prof.ControllerURL(), prof.ControllerToken())
	if err != nil {
		return fmt.Errorf("queue: %w", err)
	}
	return renderQueue(os.Stdout, qs, format)
}

func fetchControllerQueueState(ctx context.Context, baseURL, token string) (wingwire.QueueState, error) {
	u := strings.TrimRight(baseURL, "/") + "/api/v1/queue/state"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return wingwire.QueueState{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return wingwire.QueueState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return wingwire.QueueState{}, fmt.Errorf("GET queue state: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var qs wingwire.QueueState
	if err := json.NewDecoder(resp.Body).Decode(&qs); err != nil {
		return wingwire.QueueState{}, fmt.Errorf("decode queue state: %w", err)
	}
	return qs, nil
}

func warnLegacy(w io.Writer, n int) {
	if line := legacyWarningLine(n); line != "" {
		fmt.Fprintf(w, "warning: %s\n", line)
	}
}

func renderNoDaemon(w io.Writer, format string) error {
	return opsview.RenderNoDaemon(w, format)
}

func renderUnreachableDaemon(w io.Writer, format string, cause error) error {
	return opsview.RenderUnreachableDaemon(w, format, cause)
}

func renderLocalQueue(w io.Writer, qs wingwire.QueueState, format string) error {
	return opsview.RenderLocalQueue(w, qs, opsview.Serving(), format)
}

func renderQueue(w io.Writer, qs wingwire.QueueState, format string) error {
	return opsview.RenderQueue(w, qs, format)
}

func renderQueuePretty(w io.Writer, qs wingwire.QueueState) error {
	return opsview.RenderQueuePretty(w, qs)
}

func containerNote(c *wingwire.ContainerLimit) string { return opsview.ContainerNote(c) }

func budgetNote(b *wingwire.BudgetState) string { return opsview.BudgetNote(b) }

func fmtEventsLine(ev *wingwire.EventsWindow) string { return opsview.FmtEventsLine(ev) }

func fmtDaemonHeader(qs wingwire.QueueState) string { return opsview.FmtDaemonHeader(qs) }

func originWord(o wingwire.Origin) string { return opsview.OriginWord(o) }

func externalPressureNote(qs wingwire.QueueState) string { return opsview.ExternalPressureNote(qs) }
