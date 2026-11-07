package nodeproto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
)

// ExecRequest is an SDK frame asking the host to run one command. Env is the
// complete environment the SDK would have given the command, so pipeline code
// that changes its own environment keeps working; the host still applies its
// own environment rules. Without Capture the host writes output to the masked
// job log and returns only bounded tails; with Capture it returns every byte
// through [ExecOutput] lines and writes nothing to the log.
type ExecRequest struct {
	ID      string   `json:"exec"`
	Step    string   `json:"step,omitempty"`
	Argv    []string `json:"argv"`
	Dir     string   `json:"dir"`
	Env     []string `json:"env"`
	Capture bool     `json:"capture,omitempty"`
	Stdin   []byte   `json:"stdin,omitempty"`
}

// ExecCancel is an SDK frame asking the host to kill a running command. The host
// still answers the command with [ExecDone].
type ExecCancel struct {
	ID string `json:"exec_cancel"`
}

// ExecOutput is a host line on the request pipe carrying captured bytes in order.
type ExecOutput struct {
	ID     string `json:"exec_output"`
	Stream string `json:"stream"`
	Data   []byte `json:"data"`
}

// ExecDone is the host's last line for a command. ExitCode is -1 when the
// command was killed by a signal or never started; StartError explains the latter, and
// NotFound marks a missing executable. Without capture, Stdout and Stderr hold
// at most [ExecTailBytes] from the end of each stream for error messages.
type ExecDone struct {
	ID         string `json:"exec_done"`
	ExitCode   int    `json:"exit_code"`
	Signaled   bool   `json:"signaled,omitempty"`
	StartError string `json:"start_error,omitempty"`
	NotFound   bool   `json:"not_found,omitempty"`
	Stdout     []byte `json:"stdout,omitempty"`
	Stderr     []byte `json:"stderr,omitempty"`
}

// ExecTailBytes bounds each stream tail a logged command returns to the SDK.
const ExecTailBytes = 64 << 10

// HostLine is a host-initiated line on the request pipe; exactly one field is set.
type HostLine struct {
	Output *ExecOutput
	Done   *ExecDone
}

var (
	execOutputPrefix = []byte(`{"exec_output":`)
	execDonePrefix   = []byte(`{"exec_done":`)
)

// ParseHostLine reports whether line is a host-initiated line rather than a
// request. The host encodes these structs with their identifying key first, so
// a prefix check keeps the per-chunk cost of captured output to one decode.
func ParseHostLine(line []byte) (HostLine, bool, error) {
	line = bytes.TrimSpace(line)
	var target any
	var result HostLine
	switch {
	case bytes.HasPrefix(line, execOutputPrefix):
		result.Output = &ExecOutput{}
		target = result.Output
	case bytes.HasPrefix(line, execDonePrefix):
		result.Done = &ExecDone{}
		target = result.Done
	default:
		return HostLine{}, false, nil
	}
	if err := json.Unmarshal(line, target); err != nil {
		return HostLine{}, true, fmt.Errorf("%w: host exec line: %w", ErrMalformed, err)
	}
	if result.Output != nil && (result.Output.ID == "" || result.Output.Stream != "stdout" && result.Output.Stream != "stderr") {
		return HostLine{}, true, fmt.Errorf("%w: exec output needs an id and stream", ErrMalformed)
	}
	if result.Done != nil && result.Done.ID == "" {
		return HostLine{}, true, fmt.Errorf("%w: exec result needs an id", ErrMalformed)
	}
	return result, true, nil
}

var execFramePrefix = []byte(`{"exec":"`)

// perf: an exec frame carries the whole environment, so one decode replaces the general path's three
// passes; anything unusual falls back to that path.
func parseExecFast(line []byte) (Frame, bool) {
	if !bytes.HasPrefix(line, execFramePrefix) {
		return Frame{}, false
	}
	var r ExecRequest
	if json.Unmarshal(line, &r) != nil || validExec(r) != nil {
		return Frame{}, false
	}
	return Frame{Exec: &r}, true
}

func validExec(r ExecRequest) error {
	if r.ID == "" || len(r.Argv) == 0 || r.Argv[0] == "" || !filepath.IsAbs(r.Dir) || r.Env == nil {
		return fmt.Errorf("%w: exec needs an id, argv, an absolute dir and env", ErrMalformed)
	}
	return nil
}

func parseExecFrame(obj map[string]json.RawMessage, line []byte) (Frame, error) {
	if _, cancel := obj["exec_cancel"]; cancel {
		if len(obj) != 1 {
			return Frame{}, fmt.Errorf("%w: exec_cancel carries other fields", ErrMalformed)
		}
		if err := requiredString(obj, "exec_cancel"); err != nil {
			return Frame{}, err
		}
		var c ExecCancel
		if err := json.Unmarshal(line, &c); err != nil {
			return Frame{}, fmt.Errorf("%w: exec_cancel: %w", ErrMalformed, err)
		}
		return Frame{ExecCancel: &c}, nil
	}
	if err := requiredString(obj, "exec"); err != nil {
		return Frame{}, err
	}
	if err := checkProtocolValue(line, reflect.TypeFor[ExecRequest]()); err != nil {
		return Frame{}, err
	}
	var r ExecRequest
	if err := json.Unmarshal(line, &r); err != nil {
		return Frame{}, fmt.Errorf("%w: exec: %w", ErrMalformed, err)
	}
	if err := validExec(r); err != nil {
		return Frame{}, err
	}
	return Frame{Exec: &r}, nil
}
