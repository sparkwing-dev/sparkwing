package crontimer

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	// Label is the launchd label of the sparkwing cron agent.
	Label = "dev.sparkwing.crons"

	// PlistName is the file launchd reads the agent from.
	PlistName = Label + ".plist"
)

// AgentDir reports the LaunchAgents directory for a host.
func AgentDir(h Host) string {
	return filepath.Join(h.Home, "Library", "LaunchAgents")
}

func darwinPlistPath(h Host) (string, error) {
	if h.Home == "" {
		return "", errors.New("crontimer: Host.Home is required on darwin")
	}
	return filepath.Join(AgentDir(h), PlistName), nil
}

func (h Host) serviceTarget() string { return fmt.Sprintf("gui/%d/%s", h.UID, Label) }

func (h Host) domainTarget() string { return fmt.Sprintf("gui/%d", h.UID) }

func installDarwin(h Host) (State, error) {
	if err := requireBinary(h); err != nil {
		return State{}, err
	}
	path, err := darwinPlistPath(h)
	if err != nil {
		return State{}, err
	}
	plist, err := readManaged(path)
	if err != nil {
		return State{}, err
	}
	if plist.exists && !plist.ours {
		return State{Path: path, Foreign: true, Detail: foreignDetail(path)},
			fmt.Errorf("crontimer: %s was not written by sparkwing (it lacks %q); move it aside before installing", path, Marker)
	}
	if err := ensureLogDir(h.LogPath); err != nil {
		return State{Path: path}, err
	}
	if err := writeManaged(path, agentPlist(h)); err != nil {
		return State{Path: path}, err
	}

	state := State{Installed: true, Path: path, Binary: h.Binary}
	stale := bootoutStale(h)
	if out, err := h.run("launchctl", "bootstrap", h.domainTarget(), path); err != nil {
		state.Detail = "the agent's plist is written but launchd did not load it"
		return state, fmt.Errorf("crontimer: launchctl bootstrap %s %s: %w: %s%s",
			h.domainTarget(), path, err, strings.TrimSpace(out), stale)
	}
	state.Enabled = true
	state.Detail = fmt.Sprintf("%s is loaded and runs %s crons tick every minute", Label, h.Binary)
	return state, nil
}

// safety: launchd refuses to bootstrap a label already in the domain, and a first
// install has nothing loaded, so failure here is ordinary and only echoed if
// the bootstrap that follows also fails.
func bootoutStale(h Host) string {
	out, err := h.run("launchctl", "bootout", h.serviceTarget())
	if err == nil || strings.TrimSpace(out) == "" {
		return ""
	}
	return fmt.Sprintf(" (the preceding bootout said: %s)", strings.TrimSpace(out))
}

func statusDarwin(h Host) (State, error) {
	path, err := darwinPlistPath(h)
	if err != nil {
		return State{}, err
	}
	plist, err := readManaged(path)
	if err != nil {
		return State{}, err
	}
	state := State{Path: path}
	switch {
	case plist.exists && !plist.ours:
		state.Foreign = true
		state.Detail = foreignDetail(path)
		return state, nil
	case !plist.exists:
		state.Detail = "no sparkwing cron agent is installed here"
		return state, nil
	}
	state.Installed = true
	state.Binary = plistBinary([]byte(plist.body))
	state.Stale = staleAgainst(h, state.Binary)

	out, err := h.run("launchctl", "print", h.serviceTarget())
	if err == nil {
		state.Enabled = true
		state.Detail = fmt.Sprintf("%s is loaded and runs %s crons tick every minute", Label, state.Binary)
	} else {
		state.Detail = fmt.Sprintf("%s is installed but launchd does not report it loaded: %s", Label, joinOutput(out))
	}
	if state.Stale {
		state.Detail += fmt.Sprintf("; it runs %s, not %s", state.Binary, h.Binary)
	}
	return state, nil
}

func uninstallDarwin(h Host) (State, error) {
	path, err := darwinPlistPath(h)
	if err != nil {
		return State{}, err
	}
	plist, err := readManaged(path)
	if err != nil {
		return State{}, err
	}
	if plist.exists && !plist.ours {
		return State{Path: path, Foreign: true, Detail: foreignDetail(path)}, nil
	}
	if !plist.exists {
		return State{Path: path, Detail: "no sparkwing cron agent is installed here"}, nil
	}

	var bootoutErr error
	if out, err := h.run("launchctl", "bootout", h.serviceTarget()); err != nil && !notLoaded(out) {
		bootoutErr = fmt.Errorf("crontimer: launchctl bootout %s: %w: %s", h.serviceTarget(), err, strings.TrimSpace(out))
	}
	if err := removeManaged(plist); err != nil {
		return State{Path: path}, errors.Join(bootoutErr, err)
	}
	state := State{Path: path, Detail: "the sparkwing cron agent is removed"}
	if bootoutErr != nil {
		state.Detail = "the agent's plist is removed but launchd reported an error unloading it"
	}
	return state, bootoutErr
}

func agentPlist(h Host) string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	fmt.Fprintf(&b, "<!-- %s -->\n", Marker)
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	fmt.Fprintf(&b, "  <key>Label</key>\n  <string>%s</string>\n\n", xmlText(Label))
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, arg := range []string{h.Binary, "crons", "tick"} {
		fmt.Fprintf(&b, "    <string>%s</string>\n", xmlText(arg))
	}
	b.WriteString("  </array>\n\n")
	b.WriteString("  <key>StartInterval</key>\n  <integer>60</integer>\n")
	b.WriteString("  <key>RunAtLoad</key>\n  <false/>\n")
	// launchd reaps the job's process group when the tick exits unless told to
	// leave it, and the run consumer the tick starts lives in that group.
	b.WriteString("  <key>AbandonProcessGroup</key>\n  <true/>\n\n")
	if pairs := envPairs(h); len(pairs) > 0 {
		b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
		for _, kv := range pairs {
			fmt.Fprintf(&b, "    <key>%s</key>\n    <string>%s</string>\n", xmlText(kv[0]), xmlText(kv[1]))
		}
		b.WriteString("  </dict>\n\n")
	}
	if h.LogPath != "" {
		fmt.Fprintf(&b, "  <key>StandardOutPath</key>\n  <string>%s</string>\n", xmlText(h.LogPath))
		fmt.Fprintf(&b, "  <key>StandardErrorPath</key>\n  <string>%s</string>\n\n", xmlText(h.LogPath))
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func xmlText(s string) string {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(s)); err != nil {
		return ""
	}
	return buf.String()
}

// safety: the plist is the only record of the binary it runs; nothing keeps a sidecar.
func plistBinary(body []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(body))
	inKey, wantArgs := false, false
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "key":
				inKey = true
			case "string":
				if wantArgs {
					var s string
					if err := dec.DecodeElement(&s, &t); err != nil {
						return ""
					}
					return s
				}
			}
		case xml.CharData:
			if inKey && strings.TrimSpace(string(t)) == "ProgramArguments" {
				wantArgs = true
			}
		case xml.EndElement:
			if t.Name.Local == "key" {
				inKey = false
			}
		}
	}
}
