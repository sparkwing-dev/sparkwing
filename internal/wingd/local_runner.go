package wingd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

const localRunnerPath = "/api/v1/local/runners"

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
	var mu sync.Mutex
	changed := make(chan struct{})
	subscribe := func() <-chan struct{} {
		mu.Lock()
		defer mu.Unlock()
		return changed
	}
	notify := func() {
		mu.Lock()
		defer mu.Unlock()
		close(changed)
		changed = make(chan struct{})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != localRunnerPath && !strings.HasPrefix(r.URL.Path, localRunnerPath+"/") {
			next.ServeHTTP(w, r)
			return
		}
		peer, verified := localhostwire.LocalPeer(r.Context())
		owner, err := controllerPeerIdentity()
		if !verifiedSocketPeer(r.Context()) || !verified || err != nil {
			writeLoginFailure(w, &apierr.Error{Code: apierr.Unauthenticated, Message: "runner lifecycle requires a verified local socket peer"})
			return
		}
		subject, err := LocalSubject(peer, owner)
		if err != nil {
			writeLoginFailure(w, &apierr.Error{Code: apierr.Forbidden, Message: err.Error()})
			return
		}
		registry, ok := d.cfg.Runs.(LocalRunnerStore)
		if !ok {
			writeLoginFailure(w, &apierr.Error{Code: apierr.Unavailable, Message: "local runner persistence is unavailable"})
			return
		}
		if r.URL.Path == localRunnerPath && r.Method == http.MethodGet && r.URL.Query().Get("state") == "active" {
			instance, err := registry.ActiveLocalRunner(r.Context(), subject)
			if err != nil {
				writeLocalRunnerFailure(w, err)
				return
			}
			writeAdmissionJSON(w, instance)
			return
		}
		if r.URL.Path == localRunnerPath && r.Method == http.MethodPost {
			instance, err := registerLocalRunnerHTTP(w, r, registry, subject, peer)
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
			err = registry.ActivateLocalRunner(r.Context(), subject, parts[0])
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
		return instance, &apierr.Error{Code: apierr.Forbidden, Message: "runner process identity cannot be verified"}
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
		failure = &apierr.Error{Code: apierr.Internal, Message: err.Error()}
	}
	writeLoginFailure(w, failure)
}
