package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

// TrackProcess records the job process tree to measure while this lease is held.
// The lease holder's process identity continues to determine liveness.
func (l *Lease) TrackProcess(ctx context.Context, pid int) error {
	birth, err := procgroup.ProcessBirth(pid)
	if err != nil {
		return fmt.Errorf("inspect admitted job process: %w", err)
	}
	input := struct {
		PID          int    `json:"pid"`
		ProcessBirth string `json:"process_birth"`
	}{PID: pid, ProcessBirth: birth}
	return l.cl.admissionCall(ctx, http.MethodPost, "/requests/"+url.PathEscape(l.id)+"/process", input, nil, 0)
}

// TrackCommand adds a host-run command session to the job's measurement. The
// birth token is the one captured at start, so a reused PID is refused.
func (l *Lease) TrackCommand(ctx context.Context, pid int, birth string) error {
	input := struct {
		PID          int    `json:"pid"`
		ProcessBirth string `json:"process_birth"`
	}{PID: pid, ProcessBirth: birth}
	return l.cl.admissionCall(ctx, http.MethodPost, "/requests/"+url.PathEscape(l.id)+"/command", input, nil, readOnlyRetryLimit)
}
