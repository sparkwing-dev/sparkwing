package wingd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runners/service/unitfile"
)

// LocalRunnerServiceName identifies one runner instance in one state home.
func LocalRunnerServiceName(home, id string) string {
	homeSum := sha256.Sum256([]byte(filepath.Clean(home)))
	idSum := sha256.Sum256([]byte(id))
	return "sparkwing-runner-" + hex.EncodeToString(homeSum[:8]) + "-" + hex.EncodeToString(idSum[:16])
}

// RemoveLocalRunnerStartupEnvironment removes private launcher inputs after the
// service manager has loaded them. Retired identities are never restarted.
func RemoveLocalRunnerStartupEnvironment(home, id string) error {
	dir := filepath.Join(home, "runners", LocalRunnerServiceName(home, id))
	var result error
	for _, name := range []string{"runner.env", "runner.plist"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}
	return result
}

// StartLocalRunnerProcess starts an independently supervised runner. Cancellation
// bounds startup only; a launched runner drains through its own control protocol.
func StartLocalRunnerProcess(ctx context.Context, host crontimer.Host, id string) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" || strings.ContainsAny(id, "\x00\r\n") {
		return errors.New("wingd: a local runner instance is required")
	}
	for _, path := range []string{host.Home, host.SparkwingHome, host.Binary} {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
			return errors.New("wingd: runner executable and homes must be absolute single-line paths")
		}
	}
	if _, err := localRunnerEnvironment(host); err != nil {
		return err
	}
	defer func() {
		if result != nil {
			result = errors.Join(result, RemoveLocalRunnerStartupEnvironment(host.SparkwingHome, id))
		}
	}()
	switch host.GOOS {
	case "linux":
		return startLocalRunnerLinux(ctx, host, id, "/run/systemd/system")
	case "darwin":
		return startLocalRunnerDarwin(ctx, host, id)
	case "windows":
		return startUnmanagedLocalRunner(ctx, host, id)
	default:
		return fmt.Errorf("wingd: local runner supervision is unavailable on %q", host.GOOS)
	}
}

func localRunnerArguments(host crontimer.Host, id string) []string {
	return []string{host.Binary, "wingd", "runner", "--home", host.SparkwingHome, "--instance", id}
}

func localRunnerEnvironment(host crontimer.Host) ([]string, error) {
	values := maps.Clone(host.Env)
	if values == nil {
		values = make(map[string]string)
	}
	values["HOME"], values["SPARKWING_HOME"], values["PATH"] = host.Home, host.SparkwingHome, host.PathEnv
	environment := make([]string, 0, len(values))
	for _, key := range slices.Sorted(maps.Keys(values)) {
		if key == "" || strings.ContainsAny(key, "=\x00\r\n") || strings.ContainsRune(values[key], 0) {
			return nil, errors.New("wingd: invalid captured runner environment")
		}
		environment = append(environment, key+"="+values[key])
	}
	return environment, nil
}

func localRunnerDirectory(host crontimer.Host, id string) (string, error) {
	dir := filepath.Join(host.SparkwingHome, "runners", LocalRunnerServiceName(host.SparkwingHome, id))
	if err := fssecure.EnsureDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func runnerManagerCommand(ctx context.Context, host crontimer.Host, name string, args ...string) error {
	_, err := runnerManagerOutput(ctx, host, name, args...)
	return err
}

func runnerManagerOutput(ctx context.Context, host crontimer.Host, name string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var output string
	var err error
	if host.Exec != nil {
		output, err = host.Exec(ctx, name, args...)
	} else {
		environment, envErr := localRunnerEnvironment(host)
		if envErr != nil {
			return "", envErr
		}
		var body []byte
		body, err = procenv.ControlCommandContext(ctx, environment, name, args...).CombinedOutput()
		output = string(body)
	}
	if err != nil {
		return "", fmt.Errorf("wingd: runner manager %s: %w: %s", name, err, strings.TrimSpace(output))
	}
	return output, nil
}

// WaitLocalRunnerShutdown verifies that supervised Linux runners have exited after
// the controller has joined its supervisor, including processes not yet registered.
func WaitLocalRunnerShutdown(ctx context.Context, host crontimer.Host) error {
	if host.GOOS != "linux" {
		return nil
	}
	if _, err := os.Stat("/run/systemd/system"); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	name := LocalRunnerServiceName(host.SparkwingHome, "")
	prefix := name[:strings.LastIndex(name, "-")+1]
	var pending []string
	for {
		output, err := runnerManagerOutput(ctx, host, "systemctl", "--user", "list-units", "--all", "--type=service", "--output=json", "--no-pager", prefix+"*.service")
		if err != nil {
			return fmt.Errorf("local runner shutdown is incomplete; could not verify supervised units %s; retry sparkwing daemon stop: %w", strings.Join(pending, ", "), err)
		}
		var units []struct {
			Unit string `json:"unit"`
		}
		if err := json.Unmarshal([]byte(output), &units); err != nil {
			return fmt.Errorf("local runner shutdown is incomplete; could not read supervised units: %w", err)
		}
		if units == nil {
			return errors.New("local runner shutdown is incomplete; supervised unit inventory is missing")
		}
		pending = pending[:0]
		for _, unit := range units {
			if !strings.HasPrefix(unit.Unit, prefix) || !strings.HasSuffix(unit.Unit, ".service") {
				return errors.New("local runner shutdown is incomplete; unexpected supervised unit identity")
			}
			pending = append(pending, unit.Unit)
		}
		if len(pending) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("local runner shutdown is incomplete; supervised units still present: %s; let held work finish, then retry sparkwing daemon stop: %w", strings.Join(pending, ", "), ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func startLocalRunnerLinux(ctx context.Context, host crontimer.Host, id, bootPath string) error {
	info, err := os.Stat(bootPath)
	if errors.Is(err, os.ErrNotExist) {
		return startUnmanagedLocalRunner(ctx, host, id)
	}
	if err != nil {
		return fmt.Errorf("wingd: inspect systemd boot: %w", err)
	}
	if !info.IsDir() {
		return errors.New("wingd: systemd boot marker is not a directory")
	}
	if err := runnerManagerCommand(ctx, host, "systemctl", "--user", "show-environment"); err != nil {
		return err
	}
	dir, err := localRunnerDirectory(host, id)
	if err != nil {
		return err
	}
	log := filepath.Join(dir, "runner.log")
	environmentPath := filepath.Join(dir, "runner.env")
	environment, err := localRunnerEnvironment(host)
	if err != nil {
		return err
	}
	var body strings.Builder
	quote := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "$", "\\$", "`", "\\`")
	for _, assignment := range environment {
		key, value, _ := strings.Cut(assignment, "=")
		body.WriteString(key + "=\"" + quote.Replace(value) + "\"\n")
	}
	if err := fssecure.WriteFile(environmentPath, []byte(body.String())); err != nil {
		return err
	}
	args := []string{
		"--user", "--unit=" + LocalRunnerServiceName(host.SparkwingHome, id),
		"--collect", "--service-type=exec", "--property=Restart=no", "--property=TimeoutStopSec=infinity",
		"--property=KillMode=mixed",
		"--property=EnvironmentFile=" + environmentPath,
		"--property=StandardOutput=append:" + log, "--property=StandardError=append:" + log,
		"--", host.Binary,
	}
	// safety: systemd expands dollar signs in argv independently of the environment-file parser.
	for _, arg := range localRunnerArguments(host, id)[1:] {
		args = append(args, strings.ReplaceAll(arg, "$", "$$"))
	}
	return runnerManagerCommand(ctx, host, "systemd-run", args...)
}

func startLocalRunnerDarwin(ctx context.Context, host crontimer.Host, id string) error {
	dir, err := localRunnerDirectory(host, id)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "runner.plist")
	body, err := localRunnerAgent(host, id, filepath.Join(dir, "runner.log"))
	if err != nil {
		return err
	}
	if err := fssecure.WriteFile(path, []byte(body)); err != nil {
		return err
	}
	gui := runnerManagerCommand(ctx, host, "launchctl", "bootstrap", fmt.Sprintf("gui/%d", host.UID), path)
	if gui == nil {
		return nil
	}
	// safety: an account without a GUI login, such as an SSH-only Mac, has no gui domain but always has its user domain.
	if err := runnerManagerCommand(ctx, host, "launchctl", "bootstrap", fmt.Sprintf("user/%d", host.UID), path); err != nil {
		return errors.Join(gui, err)
	}
	return nil
}

func localRunnerAgent(host crontimer.Host, id, log string) (string, error) {
	environment, err := localRunnerEnvironment(host)
	if err != nil {
		return "", err
	}
	body := []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\"><dict>\n")
	body = fmt.Appendf(body, "<key>Label</key><string>%s</string>\n<key>ProgramArguments</key><array>\n", LocalRunnerServiceName(host.SparkwingHome, id))
	for _, arg := range localRunnerArguments(host, id) {
		body = fmt.Appendf(body, "<string>%s</string>\n", unitfile.XMLText(arg))
	}
	body = append(body, "</array>\n<key>EnvironmentVariables</key><dict>\n"...)
	for _, assignment := range environment {
		key, value, _ := strings.Cut(assignment, "=")
		body = fmt.Appendf(body, "<key>%s</key><string>%s</string>\n", unitfile.XMLText(key), unitfile.XMLText(value))
	}
	body = append(body, "</dict>\n<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><false/>\n"...)
	// safety: launchd's default exit timeout would kill work while the runner drains.
	body = append(body, "<key>ExitTimeOut</key><integer>0</integer>\n"...)
	body = fmt.Appendf(body, "<key>StandardOutPath</key><string>%s</string>\n<key>StandardErrorPath</key><string>%s</string>\n</dict></plist>\n", unitfile.XMLText(log), unitfile.XMLText(log))
	return string(body), nil
}
