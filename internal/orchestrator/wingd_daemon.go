package orchestrator

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/localsecrets"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// RunStoreReapInterval is how often the daemon host reaps the runs store
// while the daemon serves. It matches the controller's reaper cadence.
const RunStoreReapInterval = 10 * time.Second

// safety: a pass shares the writing handle's single connection with finalize,
// so it is bounded rather than left to the store's busy timeout, and the next
// tick retries whatever it dropped.
const runStoreReapTimeout = 30 * time.Second

// WingdOptions are the host's choices when it runs the admission daemon in
// this process. Zero values take the daemon's own defaults.
type WingdOptions struct {
	Home                  string
	Version               string
	HeadroomFraction      float64
	Budget                userconfig.Budget
	BudgetSource          wingd.BudgetSource
	BudgetOrigin          string
	AdmissionPolicy       *wingd.AdmissionPolicy
	AdmissionPolicySource string
	// Secrets seals and opens the secrets the controller API stores. Nil
	// loads this machine's keyring when the daemon starts.
	Secrets *localsecrets.Keyring
	Logger  *slog.Logger
	Logf    func(format string, args ...any)
}

// RunWingdDaemon runs the admission daemon until ctx ends or it idles out.
// The daemon reaches the runs store through one handle held for its
// lifetime, and this process reaps that store every RunStoreReapInterval
// while the daemon serves, which is the only reaper a machine with no
// controller has. Losing the election is not an error: another daemon owns
// the socket.
func RunWingdDaemon(ctx context.Context, opts WingdOptions) error {
	return runWingdDaemon(ctx, opts, nil)
}

func runWingdDaemon(ctx context.Context, opts WingdOptions, tune func(*wingd.Config, *HeldRunStore)) error {
	admissionPolicy := wingd.DefaultAdmissionPolicy()
	admissionSource := "default"
	if opts.AdmissionPolicy != nil {
		admissionPolicy = *opts.AdmissionPolicy
		admissionSource = opts.AdmissionPolicySource
	}
	ring := opts.Secrets
	if ring == nil {
		loaded, err := localsecrets.LoadKeyring(localsecrets.KeyringOptions{Create: true})
		if err != nil {
			return fmt.Errorf("local secrets key: %w", err)
		}
		ring = loaded
	}
	runs, err := NewHeldRunStore(opts.Home)
	if err != nil {
		return err
	}
	defer func() { _ = runs.Close() }()
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	api := newWingdAPI(runs, opts.Logger, ring)
	typeSafeKey := ""
	if admissionPolicy.Mode == admission.ModeJev {
		typeSafeKey = readTypeSafeAPIKey(runs.paths.StateDB(), logf)
	}
	cfg := wingd.Config{
		Home:               opts.Home,
		Version:            opts.Version,
		HeadroomFraction:   opts.HeadroomFraction,
		Budget:             opts.Budget,
		BudgetSource:       opts.BudgetSource,
		BudgetOrigin:       opts.BudgetOrigin,
		AdmissionPolicy:    &admissionPolicy,
		TypeSafeAPIKey:     typeSafeKey,
		Runs:               runs,
		StoreSchemaVersion: store.ExpectedSchemaVersion(),
		ServeAPI:           api.serve,
		Logf:               logf,
	}
	logf("admission mode %s (%s)", admissionPolicy.Mode, admissionSource)
	if tune != nil {
		tune(&cfg, runs)
	}
	d, err := wingd.New(cfg)
	if err != nil {
		return err
	}
	maintCtx, stopMaintenance := context.WithCancel(ctx)
	defer stopMaintenance()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		maintainRunStore(maintCtx, runs, d.Ready(), logf)
	}()
	runErr := d.Run(ctx)
	stopMaintenance()
	<-stopped
	if runErr != nil && !errors.Is(runErr, wingd.ErrNotElected) {
		return runErr
	}
	return nil
}

func maintainRunStore(ctx context.Context, runs *HeldRunStore, ready <-chan struct{}, logf func(string, ...any)) {
	select {
	case <-ready:
	case <-ctx.Done():
		return
	}
	fault := ""
	holder := "local-reaper:" + rand.Text()
	pass := func(force bool) {
		passCtx, cancel := context.WithTimeout(ctx, runStoreReapTimeout)
		defer cancel()
		st, err := runs.Store(passCtx, force)
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, errRunStoreAbsent) && err.Error() != fault {
				fault = err.Error()
				logf("runs store unavailable: %v", err)
			}
			return
		}
		fault = ""
		lease, err := st.AsOperator().AcquireSingletonLease(passCtx, "controller.reaper", holder, time.Minute)
		if errors.Is(err, store.ErrLockHeld) {
			return
		}
		if err != nil {
			if ctx.Err() == nil {
				logf("reaper lease: %v", err)
			}
			return
		}
		res, err := st.AsOperator().MaintainConcurrency(passCtx, lease, store.ConcurrencyMaintenanceOptions{})
		if err != nil && ctx.Err() == nil {
			logf("concurrency maintenance: %v", err)
		}
		if summary := reapSummary(res); summary != "" {
			logf("reaped %s", summary)
		}
	}
	pass(true)
	ticker := time.NewTicker(RunStoreReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pass(false)
		}
	}
}

func reapSummary(res store.ConcurrencyMaintenanceResult) string {
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, what))
		}
	}
	add(len(res.StaleHolders), "stale holder(s)")
	add(len(res.StaleWaiters), "stale waiter(s)")
	add(res.Promoted, "promoted waiter(s)")
	return strings.Join(parts, ", ")
}

// TypeSafeAPIKeySecret names the local secret the jev admission policy reads
// its TypeSafe key from.
const TypeSafeAPIKeySecret = "TYPESAFE_API_KEY"

// safety: jev admits without an advisor when it has no key, so a missing or
// unreadable secret is logged and the daemon still serves.
func readTypeSafeAPIKey(stateDB string, logf func(string, ...any)) string {
	return typeSafeAPIKeyFrom(localsecrets.StoreSource(stateDB, ""), logf)
}

// safety: a key stored from a file usually ends in a newline, which is not
// valid in the Authorization header it goes into.
func typeSafeAPIKeyFrom(src secrets.Source, logf func(string, ...any)) string {
	value, _, err := src.Read(TypeSafeAPIKeySecret)
	if err != nil {
		logf("jev admission needs the %s secret (sparkwing secrets set --name %s --file <path>): %v",
			TypeSafeAPIKeySecret, TypeSafeAPIKeySecret, err)
		return ""
	}
	return strings.TrimSpace(value)
}
