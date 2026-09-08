package main

import (
	"encoding/json"
	"fmt"
	"io"
)

type serviceStatus struct {
	Service string `json:"service"`
	State   string `json:"state"`
	PID     int    `json:"pid,omitempty"`
	Home    string `json:"home"`
	Log     string `json:"log"`
	URL     string `json:"url,omitempty"`
	API     string `json:"api,omitempty"`
}

func writeServiceStatus(w io.Writer, status serviceStatus, mode, pretty string) error {
	switch mode {
	case "json":
		return json.NewEncoder(w).Encode(status)
	case "plain":
		_, err := fmt.Fprintln(w, status.State)
		return err
	default:
		_, err := io.WriteString(w, pretty)
		return err
	}
}
