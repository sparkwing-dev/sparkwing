package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

type dashboardOptions struct {
	Addr          string `json:"addr"`
	Home          string `json:"-"`
	LogStore      string `json:"log_store,omitempty"`
	ArtifactStore string `json:"artifact_store,omitempty"`
	Profile       string `json:"profile,omitempty"`
	AllowOrigins  string `json:"allow_origins,omitempty"`
	ReadOnly      bool   `json:"read_only"`
	NoLocalStore  bool   `json:"no_local_store"`
	AllowRemote   bool   `json:"allow_remote"`
}

type dashboardArtifact struct {
	Version  string `json:"version,omitempty"`
	Revision string `json:"revision,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Path     string `json:"path,omitempty"`
}

type dashboardRecord struct {
	PID      int               `json:"pid"`
	Birth    string            `json:"birth"`
	Boot     string            `json:"boot"`
	Instance string            `json:"instance"`
	Options  dashboardOptions  `json:"options"`
	Artifact dashboardArtifact `json:"artifact"`
}

type dashboardEndpoint struct {
	Role  string `json:"role"`
	Scope string `json:"scope"`
	URL   string `json:"url"`
}
type dashboardBuild struct {
	Status    string            `json:"status"`
	Running   dashboardArtifact `json:"running"`
	Installed dashboardArtifact `json:"installed"`
}
type dashboardResult struct {
	Kind      string              `json:"kind"`
	Tool      string              `json:"tool"`
	Service   string              `json:"service"`
	Action    string              `json:"action"`
	Outcome   string              `json:"outcome"`
	State     string              `json:"state"`
	PID       int                 `json:"pid,omitempty"`
	Ownership string              `json:"ownership"`
	Bind      string              `json:"bind,omitempty"`
	Endpoints []dashboardEndpoint `json:"endpoints"`
	Log       string              `json:"log"`
	Home      string              `json:"home"`
	URL       string              `json:"url,omitempty"`
	API       string              `json:"api,omitempty"`
	Readiness string              `json:"readiness"`
	Build     dashboardBuild      `json:"build"`
	ReadOnly  bool                `json:"read_only"`
	Warning   string              `json:"warning,omitempty"`
}

func dashboardStatePath(dp dashboardPaths) string {
	return filepath.Join(dp.home, "dashboard-state.json")
}

func readDashboardRecord(dp dashboardPaths) (dashboardRecord, error) {
	var record dashboardRecord
	f, err := fssecure.OpenPrivateConfig(dashboardStatePath(dp))
	if err != nil {
		return record, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil {
		return record, err
	}
	if len(b) > 64*1024 {
		return record, errors.New("dashboard state exceeds 64 KiB")
	}
	if err = json.Unmarshal(b, &record); err != nil {
		return record, errors.New("invalid dashboard state; inspect the private state file")
	}
	if record.PID <= 0 || record.Birth == "" || record.Boot == "" || record.Instance == "" {
		return record, errors.New("dashboard state lacks process ownership identity")
	}
	return record, nil
}

func writeDashboardRecord(dp dashboardPaths, r dashboardRecord) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dp.home, ".dashboard-state-*")
	if err != nil {
		return err
	}
	defer func() { dashboardCleanupError("remove temporary dashboard state", os.Remove(f.Name())) }()
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), dashboardStatePath(dp))
}

func dashboardOwned(r dashboardRecord) (bool, error) {
	if !processAlive(r.PID) {
		return false, nil
	}
	boot, err := dashboardBoot()
	if err != nil {
		return false, err
	}
	birth, err := procgroup.ProcessBirth(r.PID)
	if err != nil {
		return false, err
	}
	if r.Boot != boot || r.Birth != birth {
		return false, errors.New("dashboard PID belongs to another process incarnation")
	}
	return true, nil
}

func dashboardFileArtifact(path string) dashboardArtifact {
	result := dashboardArtifact{Path: path}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return result
	}
	f, err := openUpdateInput(path)
	if err != nil {
		return result
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return result
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return result
	}
	after, err := f.Stat()
	if err != nil || info.Size() != after.Size() || info.ModTime() != after.ModTime() {
		return result
	}
	result.SHA256 = hex.EncodeToString(h.Sum(nil))
	metadata := artifactIdentityFromFile(f, path)
	result.Version = metadata.Version
	result.Revision = metadata.Revision
	return result
}

func inspectDashboard(dp dashboardPaths, action string) (dashboardResult, dashboardRecord, error) {
	installed := dashboardArtifact{}
	if path, err := resolveUpdateDestination(); err == nil {
		installed = dashboardFileArtifact(path)
	}
	out := dashboardResult{Kind: "service", Tool: "sparkwing", Service: "dashboard", Action: action, Outcome: "observed", State: "stopped", Ownership: "unknown", Endpoints: []dashboardEndpoint{}, Home: dp.home, Log: dp.log, Readiness: "not_ready", Build: dashboardBuild{Status: "unknown", Installed: installed}}
	record, err := readDashboardRecord(dp)
	if err != nil {
		pid, live, pidErr := dashboardLegacyPID(dp.pid)
		if pidErr != nil {
			out.State = "unknown"
			out.Outcome = "unverified"
			out.Readiness = "unknown"
			return out, record, pidErr
		}
		if live {
			out.PID = pid
			out.State = "unknown"
			out.Readiness = "unknown"
			out.Outcome = "unverified"
			out.Warning = "A running PID has no verified dashboard identity; it was left unchanged."
			return out, record, err
		}
		if !errors.Is(err, os.ErrNotExist) {
			out.State = "unknown"
			out.Readiness = "unknown"
			out.Outcome = "unverified"
			return out, record, err
		}
		return out, record, nil
	}
	owned, err := dashboardOwned(record)
	if err != nil {
		out.PID = record.PID
		out.State = "unknown"
		out.Readiness = "unknown"
		out.Outcome = "unverified"
		return out, record, err
	}
	if !owned {
		return out, record, nil
	}
	out.PID = record.PID
	out.State = "running"
	out.Ownership = "owned"
	out.Bind = record.Options.Addr
	out.ReadOnly = record.Options.ReadOnly
	out.Build.Running = record.Artifact
	if record.Artifact.SHA256 != "" && installed.SHA256 != "" {
		out.Build.Status = "different"
		if record.Artifact.SHA256 == installed.SHA256 {
			out.Build.Status = "match"
		}
	}
	host, port, err := net.SplitHostPort(out.Bind)
	if err == nil {
		scope := "configured"
		if ip := net.ParseIP(host); host == "localhost" || ip != nil && ip.IsLoopback() {
			scope = "loopback"
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			if host == "::" {
				host = "::1"
			} else {
				host = "127.0.0.1"
			}
			scope = "loopback"
		}
		out.URL = "http://" + net.JoinHostPort(host, port)
		out.API = out.URL + "/api/v1"
		out.Endpoints = []dashboardEndpoint{{"ui", scope, out.URL}, {"api", scope, out.API}}
		if version, ok := getDashboardVersion(dashboardHTTPClient(), out.URL); ok && version.PID == record.PID && version.Instance == record.Instance {
			out.Readiness = "ready"
		}
	}
	return out, record, nil
}

func renderDashboard(out dashboardResult, mode string) error {
	switch mode {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(out)
	case "plain":
		_, err := fmt.Fprintln(os.Stdout, out.State)
		return err
	default:
		var b strings.Builder
		if out.URL != "" {
			fmt.Fprintf(&b, "dashboard: %s\napi:       %s\n", out.URL, out.API)
		}
		fmt.Fprintf(&b, "%s (%s); readiness: %s; build: %s\n", out.State, out.Outcome, out.Readiness, out.Build.Status)
		if out.PID > 0 {
			fmt.Fprintf(&b, "pid: %d (%s)\n", out.PID, out.Ownership)
		}
		fmt.Fprintf(&b, "home: %s\nlog:  %s\n", out.Home, out.Log)
		if out.Warning != "" {
			fmt.Fprintf(&b, "warning: %s\n", out.Warning)
		}
		_, err := fmt.Fprint(os.Stdout, b.String())
		return err
	}
}

func waitDashboard(dp dashboardPaths, pid int, exited <-chan struct{}) (dashboardResult, error) {
	deadline := time.Now().Add(dashboardStartTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return dashboardResult{}, errors.New("dashboard exited during startup")
		default:
		}
		out, _, err := inspectDashboard(dp, "start")
		if err == nil && out.PID == pid && out.Readiness == "ready" {
			return out, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return dashboardResult{}, errors.New("dashboard did not confirm owned HTTP readiness within 30s")
}

func dashboardOpenArtifact(f *os.File) dashboardArtifact {
	result := dashboardArtifact{}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return result
	}
	result.SHA256 = hex.EncodeToString(h.Sum(nil))
	result.Version = installedVersion()
	identity := readInvokingUpdateIdentity()
	result.Revision = identity.Revision
	return result
}

func dashboardLegacyPID(path string) (int, bool, error) {
	f, err := fssecure.OpenPrivateConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64))
	if err != nil {
		return 0, false, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false, errors.New("invalid legacy dashboard PID; no process was changed")
	}
	return pid, processAlive(pid), nil
}
