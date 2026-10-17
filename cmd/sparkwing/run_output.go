package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

func emitLocalRunEvent(out io.Writer, format, event, run, pipeline, status string) error {
	attrs := map[string]string{"run_id": run, "pipeline": pipeline}
	if status != "" {
		attrs["status"] = status
		attrs["status_command"] = "sparkwing runs status --run " + run
		attrs["logs_command"] = "sparkwing runs logs --run " + run
	}
	if format == "json" {
		return json.NewEncoder(out).Encode(struct {
			Time  time.Time         `json:"ts"`
			Event string            `json:"event"`
			Attrs map[string]string `json:"attrs"`
		}{time.Now().UTC(), event, attrs})
	}
	if status == "" {
		_, err := fmt.Fprintf(out, "run %s: %s started\n", run, pipeline)
		return err
	}
	_, err := fmt.Fprintf(out, "run %s: %s %s\n", run, pipeline, status)
	return err
}
