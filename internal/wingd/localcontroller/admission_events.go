package localcontroller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// RecordAdmissionEvent projects this controller's machine decisions into its run stream.
func (c *localController) RecordAdmissionEvent(holder, kind string, data map[string]any) error {
	ref := wingwire.HolderRef(holder)
	run := ref.RunID()
	if run.Remote() {
		return nil
	}
	encoded, ok := strings.CutPrefix(holder, string(run)+"/node-host/")
	if !ok {
		return nil
	}
	node, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}
	if len(node) == 0 {
		return nil
	}
	event := ""
	switch kind {
	case "queued":
		event = runstate.EventAdmissionQueued
	case "grant":
		event = runstate.EventAdmissionGranted
	default:
		return nil
	}
	// safety: journal fields include other runs and process identities; only this job's wait facts are public.
	payload := map[string]any{}
	for _, key := range []string{"position", "key", "resource", "blocking_reason", "wait_ms"} {
		if value, ok := data[key]; ok {
			payload[key] = value
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	err = c.tenant.AppendEvent(context.Background(), string(run), string(node), event, raw)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}
