package runners

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

// safety: the marker sits beside the runner credential, which both the runner service and the
// owner's CLI locate through the runner's machine config, whatever SPARKWING_HOME each uses.
const pauseMarker = "paused"

func pausePath(credentialsDir string) (string, error) {
	if strings.TrimSpace(credentialsDir) == "" {
		return "", errors.New("runner credentials_dir is required")
	}
	return filepath.Join(credentialsDir, pauseMarker), nil
}

// Paused reports whether the owner paused the runner whose credential lives in credentialsDir.
func Paused(credentialsDir string) (bool, error) {
	path, err := pausePath(credentialsDir)
	if err != nil {
		return false, err
	}
	_, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// SetPaused pauses or resumes the runner and reports whether the state changed.
func SetPaused(credentialsDir string, paused bool) (bool, error) {
	path, err := pausePath(credentialsDir)
	if err != nil {
		return false, err
	}
	was, err := Paused(credentialsDir)
	if err != nil || was == paused {
		return false, err
	}
	if paused {
		return true, fssecure.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"))
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}

// PausableController offers no free slots while the runner is paused, so it claims
// nothing new and finishes held work. It keeps polling, so the controller still sees it.
type PausableController struct {
	controllerwire.Controller
	CredentialsDir string
	state          *pauseState
}

type pauseState struct {
	mu     sync.Mutex
	paused bool
}

// NewPausableController wraps controller with the owner's local pause.
func NewPausableController(controller controllerwire.Controller, credentialsDir string) PausableController {
	return PausableController{Controller: controller, CredentialsDir: credentialsDir, state: &pauseState{}}
}

func (c PausableController) Claim(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	paused, err := Paused(c.CredentialsDir)
	if err != nil {
		// safety: an unreadable marker reads as paused; the owner asked for no new work or the state is unknown.
		paused = true
	}
	c.state.mu.Lock()
	changed := paused != c.state.paused
	c.state.paused = paused
	c.state.mu.Unlock()
	switch {
	case changed && paused:
		slog.Warn("runner paused: it claims no new work and finishes held jobs; run sparkwing runners resume to continue", "err", err)
	case changed:
		slog.Info("runner resumed: it claims work again")
	}
	if paused {
		request.FreeSlots = new(int)
	}
	return c.Controller.Claim(ctx, request)
}
