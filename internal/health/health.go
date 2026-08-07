// Package health decodes the response body every sparkwing service
// publishes on its health endpoint, so that each consumer of that
// contract reaches the same verdict about the same service.
//
// The contract deliberately reports partial failure in the body while
// still answering HTTP 200: only a total outage (an unreachable
// database, a process that is down) turns into a non-2xx status. A
// filling log volume, a stalled background fetch loop, an unwritable
// cache directory, and a controller whose runs are mostly failing all
// arrive as `{"status":"degraded","problems":[...]}` with a 200. A
// consumer that classifies on the status code alone therefore shows a
// healthy service in exactly the conditions these endpoints were
// written to surface.
package health

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// StatusOK and StatusDegraded are the two values services put in the
// `status` field. A body carrying neither is treated as healthy,
// because a service that publishes no self-diagnosis has not claimed
// anything is wrong.
const (
	StatusOK       = "ok"
	StatusDegraded = "degraded"
)

// MaxBodyBytes bounds how much of a health body is read. Health
// payloads are a status word and a handful of problem strings; a
// larger body is a misconfigured endpoint, not a health report.
const MaxBodyBytes = 1 << 16

// Response is the uniform shape a sparkwing service returns from its
// health endpoint. Services that do not self-diagnose leave Problems
// empty; services that do (controller, logs, cache) surface
// "component: detail" strings so tooling can name the fault without
// declaring a blanket outage.
type Response struct {
	Status   string   `json:"status"`
	Problems []string `json:"problems,omitempty"`
	// Auth is "enabled" or "disabled"; the controller sets it so
	// tooling can warn when a deployment is serving open. Empty from
	// services that do not report it.
	Auth string `json:"auth,omitempty"`
}

// Degraded reports whether the body says the service is not fully
// healthy -- either by naming the state or by listing problems.
func (r Response) Degraded() bool {
	return r.Status == StatusDegraded || len(r.Problems) > 0
}

// ErrNotContract reports a body that arrived intact but is not this
// JSON contract -- a service answering 200 with plain text, say. It is
// separated from a read failure because the two mean opposite things: a
// service outside the contract has told us it is up, while a body we
// could not finish reading told us nothing at all.
var ErrNotContract = errors.New("health body is not the JSON contract")

// Decode reads a bounded prefix of a health response body and parses
// it. Both failure modes return the zero Response; callers that treat
// them differently test the error with errors.Is(err, ErrNotContract).
func Decode(r io.Reader) (Response, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxBodyBytes))
	if err != nil {
		return Response{}, fmt.Errorf("read health body: %w", err)
	}
	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrNotContract, err)
	}
	return out, nil
}
