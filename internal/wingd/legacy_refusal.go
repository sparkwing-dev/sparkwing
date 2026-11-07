package wingd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// safety: old pinned hosts must receive an actionable refusal before any work can start.
func (d *Daemon) refuseLegacyAdmission(connection net.Conn) {
	defer func() {
		if err := connection.Close(); err != nil {
			d.cfg.logf("close refused peer: %v", err)
		}
	}()
	if connection.SetDeadline(time.Now().Add(5*time.Second)) != nil {
		return
	}
	scanner := bufio.NewScanner(connection)
	scanner.Buffer(make([]byte, 4096), maxFrame)
	if !scanner.Scan() {
		return
	}
	var hello struct {
		Type    string `json:"type"`
		Payload struct {
			ProtocolMajor int    `json:"protocol_major"`
			BinaryVersion string `json:"binary_version"`
			BuildIdentity string `json:"build_identity"`
		} `json:"payload"`
	}
	if json.Unmarshal(scanner.Bytes(), &hello) != nil || hello.Type != "hello" {
		return
	}
	old := hello.Payload.BinaryVersion
	if old == "" {
		old = "(unknown)"
	}
	current := d.cfg.Version
	if current == "" {
		current = "(unknown)"
	}
	// safety: pipeline binaries and sparkwing-runner with runner.local_admission share this hello, and an old
	// peer prints only this text, so it names both remedies.
	reason := fmt.Sprintf("sparkwing %s uses connection-based machine admission; controller %s requires durable HTTP admission. Run `sparkwing pipeline upgrade` to update this pipeline pin; for sparkwing-runner, install the same build as this controller and restart its service, or set runner.local_admission: false and restart the runner", old, current)
	d.cfg.logf("refused machine admission from sparkwing %s: %s", old, reason)
	d.recordJournal("handshake_refused", nil, map[string]any{"peer_version": old, "reason": reason})
	encoder := json.NewEncoder(connection)
	if encoder.Encode(map[string]any{"type": "hello_ack", "payload": map[string]any{"protocol_major": hello.Payload.ProtocolMajor, "native_protocol_major": ProtocolMajor, "binary_version": current, "build_identity": hello.Payload.BuildIdentity, "api_ready": false, "api_error": reason, "store_ready": false, "store_error": reason}}) != nil {
		return
	}
	if !scanner.Scan() {
		return
	}
	if err := encoder.Encode(map[string]any{"type": "unsupported", "payload": map[string]any{"type": reason}}); err != nil {
		d.cfg.logf("write obsolete admission refusal: %v", err)
	}
}
