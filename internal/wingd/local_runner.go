package wingd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

const localRunnerPath = "/api/v1/local/runners"

// safety: a runner holding jobs keeps running past this bound and finishes them against the next controller.
const localRunnerRetireWait = 5 * time.Second

type runnerWake struct {
	mu      sync.Mutex
	changed chan struct{}
}

func (w *runnerWake) subscribe() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.changed == nil {
		w.changed = make(chan struct{})
	}
	return w.changed
}

func (w *runnerWake) notify() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.changed != nil {
		close(w.changed)
	}
	w.changed = make(chan struct{})
}

// LocalRunnerProcessAlive checks the recorded process incarnation, excluding exited processes.
// Inspection errors leave the identity unknown rather than declaring its process dead.
func LocalRunnerProcessAlive(instance localhostwire.LocalRunnerInstance) (bool, error) {
	birth, err := admissionProcessBirth(instance.PID)
	if errors.Is(err, procgroup.ErrProcessAbsent) {
		return false, nil
	}
	return err == nil && birth == instance.ProcessBirth, err
}

// LocalRunnerStore persists process instances and arbitrates activation with claims.
type LocalRunnerStore interface {
	ActiveLocalRunner(context.Context, string) (localhostwire.LocalRunnerInstance, error)
	RegisterLocalRunner(context.Context, string, localhostwire.LocalRunnerInstance) (localhostwire.LocalRunnerInstance, error)
	ReadLocalRunner(context.Context, string, string) (localhostwire.LocalRunnerInstance, error)
	ActivateLocalRunner(context.Context, string, string) error
	DrainLocalRunner(context.Context, string, string) error
	StopLocalRunner(context.Context, string, string) error
}

func (d *Daemon) localRunnerHTTP(next http.Handler) http.Handler {
	subscribe, notify := d.runnerWake.subscribe, d.runnerWake.notify
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != localRunnerPath && !strings.HasPrefix(r.URL.Path, localRunnerPath+"/") {
			next.ServeHTTP(w, r)
			return
		}
		peer, verified := localhostwire.LocalPeer(r.Context())
		owner, err := controllerPeerIdentity()
		if !verifiedSocketPeer(r.Context()) || !verified || err != nil {
			writeLoginFailure(w, &apierr.Error{Code: hosterr.Unauthenticated, Message: "runner lifecycle requires a verified local socket peer"})
			return
		}
		subject, err := LocalSubject(peer, owner)
		if err != nil {
			writeLoginFailure(w, &apierr.Error{Code: hosterr.Forbidden, Message: err.Error()})
			return
		}
		registry, ok := d.cfg.Runs.(LocalRunnerStore)
		if !ok {
			writeLoginFailure(w, &apierr.Error{Code: hosterr.Unavailable, Message: "local runner persistence is unavailable"})
			return
		}
		if r.URL.Path == localRunnerPath && r.Method == http.MethodGet && r.URL.Query().Get("state") == "active" {
			// safety: a stopping controller must not hand its supervisor a reason to start a runner it then abandons.
			if d.isDraining() {
				writeLocalRunnerFailure(w, errControllerStopping)
				return
			}
			instance, err := registry.ActiveLocalRunner(r.Context(), subject)
			if err != nil {
				writeLocalRunnerFailure(w, err)
				return
			}
			writeAdmissionJSON(w, instance)
			return
		}
		if r.URL.Path == localRunnerPath && r.Method == http.MethodPost {
			release, ok := d.admitRunnerChange()
			if !ok {
				writeLocalRunnerFailure(w, errControllerStopping)
				return
			}
			instance, err := registerLocalRunnerHTTP(w, r, registry, subject, peer)
			release()
			if err != nil {
				writeLocalRunnerFailure(w, err)
				return
			}
			notify()
			writeAdmissionJSON(w, instance)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, localRunnerPath+"/"), "/")
		if len(parts) == 1 && validLocalRunnerID(parts[0]) && r.Method == http.MethodGet {
			instance, err := watchLocalRunnerHTTP(r.Context(), registry, subject, parts[0], r.URL.Query().Get("wait_state"), subscribe)
			if err != nil {
				writeLocalRunnerFailure(w, err)
				return
			}
			writeAdmissionJSON(w, instance)
			return
		}
		if len(parts) != 2 || !validLocalRunnerID(parts[0]) || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		switch parts[1] {
		case "activate":
			release, ok := d.admitRunnerChange()
			if !ok {
				writeLocalRunnerFailure(w, errControllerStopping)
				return
			}
			err = registry.ActivateLocalRunner(r.Context(), subject, parts[0])
			release()
		case "drain":
			err = registry.DrainLocalRunner(r.Context(), subject, parts[0])
		case "stop":
			err = registry.StopLocalRunner(r.Context(), subject, parts[0])
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			writeLocalRunnerFailure(w, err)
			return
		}
		notify()
		writeAdmissionJSON(w, struct{}{})
	})
}

// safety: the drain lands while the API still answers, so the runner sees it, records its stop and revokes its
// token before exiting; a runner that outlives its controller cannot. Restart skips this because runners and
// their jobs outlive controller restarts.
func (d *Daemon) retireLocalRunner(ctx context.Context) error {
	registry, ok := d.cfg.Runs.(LocalRunnerStore)
	if !ok {
		return nil
	}
	subject, err := CurrentLocalSubject()
	if err != nil {
		return err
	}
	d.runnerChanges.lock()
	instance, err := registry.ActiveLocalRunner(ctx, subject)
	var failure *apierr.Error
	if err == nil {
		err = registry.DrainLocalRunner(ctx, subject, instance.ID)
	}
	d.runnerChanges.unlock()
	if errors.As(err, &failure) && failure.Code == apierr.NotFound {
		return nil
	}
	if err != nil {
		return err
	}
	d.runnerWake.notify()
	ctx, cancel := context.WithTimeout(ctx, localRunnerRetireWait)
	defer cancel()
	for {
		alive, err := LocalRunnerProcessAlive(instance)
		if err != nil || !alive {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("local runner %s (pid %d) is still running; controller shutdown is incomplete; run sparkwing dashboard open to let held work finish, then retry sparkwing daemon stop: %w", instance.ID, instance.PID, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

var errControllerStopping = &apierr.Error{Code: hosterr.Unavailable, Message: "the local controller is stopping"}

func (d *Daemon) isDraining() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.draining
}

// safety: registration and activation hold runnerChanges through persistence and check draining under it, so a
// retirement, which drains while holding it after draining is set, sees every change that passed and refuses the rest.
func (d *Daemon) admitRunnerChange() (func(), bool) {
	d.runnerChanges.lock()
	if d.isDraining() {
		d.runnerChanges.unlock()
		return nil, false
	}
	return d.runnerChanges.unlock, true
}

// safety: a one-slot channel rather than a mutex, so testing/synctest can see a waiter as durably blocked.
type runnerGate struct {
	once sync.Once
	slot chan struct{}
}

func (g *runnerGate) lock() {
	g.once.Do(func() { g.slot = make(chan struct{}, 1) })
	g.slot <- struct{}{}
}

func (g *runnerGate) unlock() { <-g.slot }

func validLocalRunnerID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\?#\x00\r\n")
}

func registerLocalRunnerHTTP(w http.ResponseWriter, r *http.Request, registry LocalRunnerStore, subject string, peer localhostwire.LocalPeerIdentity) (localhostwire.LocalRunnerInstance, error) {
	var instance localhostwire.LocalRunnerInstance
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return instance, &apierr.Error{Code: apierr.BadRequest, Message: "runner registration requires application/json"}
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxFrame))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&instance) != nil || decoder.Decode(new(any)) != io.EOF || !validLocalRunnerID(instance.ID) || instance.PID <= 0 || instance.ProcessBirth == "" || instance.State != "" || instance.Machine != "" {
		return instance, &apierr.Error{Code: apierr.BadRequest, Message: "runner registration requires one process identity and no lifecycle state"}
	}
	if err := verifyAdmissionProcess(peer, instance.PID, instance.ProcessBirth); err != nil {
		return instance, &apierr.Error{Code: hosterr.Forbidden, Message: "runner process identity cannot be verified"}
	}
	return registry.RegisterLocalRunner(r.Context(), subject, instance)
}

func watchLocalRunnerHTTP(ctx context.Context, registry LocalRunnerStore, subject, id, previous string, subscribe func() <-chan struct{}) (localhostwire.LocalRunnerInstance, error) {
	if previous != "" && previous != "registered" && previous != "active" && previous != "draining" && previous != "stopped" {
		return localhostwire.LocalRunnerInstance{}, &apierr.Error{Code: apierr.BadRequest, Message: "unknown runner lifecycle state"}
	}
	timer := time.NewTimer(admissionPollDuration)
	defer timer.Stop()
	for {
		changed := subscribe()
		instance, err := registry.ReadLocalRunner(ctx, subject, id)
		if err != nil || previous == "" || instance.State != previous {
			return instance, err
		}
		select {
		case <-changed:
		case <-timer.C:
			return instance, nil
		case <-ctx.Done():
			return localhostwire.LocalRunnerInstance{}, ctx.Err()
		}
	}
}

func writeLocalRunnerFailure(w http.ResponseWriter, err error) {
	var failure *apierr.Error
	if !errors.As(err, &failure) {
		failure = &apierr.Error{Code: hosterr.Internal, Message: err.Error()}
	}
	writeLoginFailure(w, failure)
}
