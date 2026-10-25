package crontimer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runners/service/unitfile"
)

const defaultLabel = "dev.sparkwing.crons"

func (h Host) label() string {
	if i := h.instance(); i != "" {
		return defaultLabel + "." + i
	}
	return defaultLabel
}

func (h Host) plistName() string { return h.label() + ".plist" }

// AgentDir reports the LaunchAgents directory for a host.
func AgentDir(h Host) string {
	return filepath.Join(h.Home, "Library", "LaunchAgents")
}

func darwinPlistPath(h Host) (string, error) {
	if h.Home == "" {
		return "", errors.New("crontimer: Host.Home is required on darwin")
	}
	return filepath.Join(AgentDir(h), h.plistName()), nil
}

func (h Host) serviceTarget() string { return fmt.Sprintf("gui/%d/%s", h.UID, h.label()) }

func (h Host) domainTarget() string { return fmt.Sprintf("gui/%d", h.UID) }

func installDarwin(ctx context.Context, h Host) (State, error) {
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
	if plist.exists && timerHome(unitfile.PlistArguments([]byte(plist.body))) != h.SparkwingHome {
		return State{}, errors.New(otherHomeDetail(h, path))
	}
	loaded, err := refreshPreflight(ctx, h, path)
	if err != nil {
		return State{}, err
	}
	if err := ensureLogDir(h.LogPath); err != nil {
		return State{Path: path}, err
	}
	if err := writeManaged(path, agentPlist(h)); err != nil {
		return State{Path: path}, err
	}

	state := commandState(h, path, agentPlist(h))
	if loaded {
		if err := runRefresh(ctx, h, "launchctl", "bootout", h.serviceTarget()); err != nil {
			return state, err
		}
	}
	if out, err := h.run(ctx, "launchctl", "bootstrap", h.domainTarget(), path); err != nil {
		state.Detail = "the agent's plist is written but launchd did not load it"
		return state, fmt.Errorf("crontimer: launchctl bootstrap %s %s: %w: %s",
			h.domainTarget(), path, err, strings.TrimSpace(out))
	}
	state.Enabled = true
	state.Detail = fmt.Sprintf("%s is loaded and runs %s __tick every minute", h.label(), h.Binary)
	return state, nil
}

func uninstallDarwin(ctx context.Context, h Host) (State, error) {
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
	if timerHome(unitfile.PlistArguments([]byte(plist.body))) != h.SparkwingHome {
		return State{}, errors.New(otherHomeDetail(h, path))
	}
	loaded, err := refreshPreflight(ctx, h, path)
	if err != nil {
		return State{}, err
	}

	var bootoutErr error
	if loaded {
		if out, err := h.run(ctx, "launchctl", "bootout", h.serviceTarget()); err != nil && !notLoaded(out) {
			bootoutErr = fmt.Errorf("crontimer: launchctl bootout %s: %w: %s", h.serviceTarget(), err, strings.TrimSpace(out))
		}
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
	fmt.Fprintf(&b, "<!-- %s -->\n%s\n", Marker, unitfile.VersionRecord("darwin", h.Version))
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	fmt.Fprintf(&b, "  <key>Label</key>\n  <string>%s</string>\n\n", unitfile.XMLText(h.label()))
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, arg := range timerCommand(h) {
		fmt.Fprintf(&b, "    <string>%s</string>\n", unitfile.XMLText(arg))
	}
	b.WriteString("  </array>\n\n")
	b.WriteString("  <key>StartInterval</key>\n  <integer>60</integer>\n")
	b.WriteString("  <key>RunAtLoad</key>\n  <false/>\n")
	// safety: __tick ensures the local controller, then invokes its tick.
	// Keep that controller alive after launchd reaps the tick process.
	b.WriteString("  <key>AbandonProcessGroup</key>\n  <true/>\n\n")
	if pairs := envPairs(h); len(pairs) > 0 {
		b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
		for _, kv := range pairs {
			fmt.Fprintf(&b, "    <key>%s</key>\n    <string>%s</string>\n", unitfile.XMLText(kv[0]), unitfile.XMLText(kv[1]))
		}
		b.WriteString("  </dict>\n\n")
	}
	if h.LogPath != "" {
		fmt.Fprintf(&b, "  <key>StandardOutPath</key>\n  <string>%s</string>\n", unitfile.XMLText(h.LogPath))
		fmt.Fprintf(&b, "  <key>StandardErrorPath</key>\n  <string>%s</string>\n\n", unitfile.XMLText(h.LogPath))
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}
