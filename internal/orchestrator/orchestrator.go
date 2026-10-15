package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type DebugDirectives struct {
	PauseBefore    []string
	PauseAfter     []string
	PauseOnFailure bool
}

type Result struct {
	RunID  string
	Status string
	Error  error
}

func canceledByRun(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	// safety: a ctx-cancelled child is SIGKILLed; its ExitError carries
	// no wrapped context error, only the "signal: killed" text.
	return strings.Contains(err.Error(), "signal: killed")
}

func newRunID() string {
	ts := time.Now().UTC().Format("20060102-150405")
	var suffix [16]byte
	_, _ = rand.Read(suffix[:])
	return fmt.Sprintf("run-%s-%s", ts, hex.EncodeToString(suffix[:]))
}
