// `sparkwing queue` -- the one truthful view of local admission. It
// reads the local daemon's queue state and renders every resource with
// its capacity and in-use amount, every holder with elapsed time and
// cost, and every waiter in arrival order with what it is waiting on. A
// holder that is alive but idle while runs queue behind it is flagged
// with the exact non-destructive recovery command. With no daemon
// running there is nothing to arbitrate, so the command reports an empty
// queue and exits 0. Rendering is shared with the headless pipeline
// binary through internal/opsview so both present an identical view.
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

	// safety: empty Version keeps this read-only view from ever draining
	// or replacing a running daemon during the version handshake.
	qs, err := wingdclient.Query(ctx, wingdclient.Options{Home: *home})
	legacy, _ := liveLegacyBoxSlots(*home)

	if err != nil {
		if errors.Is(err, wingdclient.ErrNoDaemon) {
			if rerr := renderNoDaemon(os.Stdout, format); rerr != nil {
				return rerr
			}
			warnLegacy(os.Stderr, len(legacy))
			return nil
		}
		return fmt.Errorf("queue: %w", err)
	}
	if rerr := renderQueue(os.Stdout, qs, format); rerr != nil {
		return rerr
	}
	warnLegacy(os.Stderr, len(legacy))
	return nil
}

// runQueueProfile inspects a controller's admission state through the same
// renderer as the local view: it fetches the controller's QueueState-shaped
// endpoint and hands it to the one queue renderer, so an operator reads one
// vocabulary regardless of where admission lives.
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

// fetchControllerQueueState calls GET /api/v1/queue/state with bearer auth and
// decodes the controller's unified admission view.
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

// warnLegacy prints the legacy-coexistence warning to stderr when
// older-pinned pipeline binaries are still admitting outside the daemon.
// It goes to stderr so JSON and plain stdout stay machine-clean.
func warnLegacy(w io.Writer, n int) {
	if line := legacyWarningLine(n); line != "" {
		fmt.Fprintf(w, "warning: %s\n", line)
	}
}

func renderNoDaemon(w io.Writer, format string) error {
	return opsview.RenderNoDaemon(w, format)
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
