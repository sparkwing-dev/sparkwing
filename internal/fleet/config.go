package fleet

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const Filename = "fleet.yaml"

var executorNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type Config struct {
	Listen           string     `yaml:"listen"`
	PublicURL        string     `yaml:"public_url"`
	AllowTailnetHTTP bool       `yaml:"allow_tailnet_http,omitempty"`
	Local            Local      `yaml:"local"`
	Executors        []Executor `yaml:"executors,omitempty"`
}

type Local struct {
	Name          string   `yaml:"name,omitempty"`
	Capabilities  []string `yaml:"capabilities,omitempty"`
	MaxConcurrent int      `yaml:"max_concurrent"`
	Contribution  string   `yaml:"contribution"`
	LocalReserve  string   `yaml:"local_reserve,omitempty"`
}

type Executor struct {
	Name            string                 `yaml:"name"`
	Location        string                 `yaml:"location"`
	Capabilities    []string               `yaml:"capabilities,omitempty"`
	BasePriority    int                    `yaml:"base_priority"`
	PriorityCeiling int                    `yaml:"priority_ceiling"`
	MaxConcurrent   int                    `yaml:"max_concurrent"`
	Budget          store.ExecutorResource `yaml:"budget"`
}

type TailscaleIPs func() ([]netip.Addr, error)

func (l *Local) UnmarshalYAML(node *yaml.Node) error {
	type plain Local
	*l = Local{MaxConcurrent: 1, Contribution: "50%,50%"}
	return node.Decode((*plain)(l))
}

func (e *Executor) UnmarshalYAML(node *yaml.Node) error {
	type plain Executor
	*e = Executor{BasePriority: 50, PriorityCeiling: 100, MaxConcurrent: 1}
	return node.Decode((*plain)(e))
}

func DefaultPath() (string, error) { return fssecure.ConfigFile(Filename) }

func Load(path string, tailscaleIPs TailscaleIPs) (Config, error) {
	f, err := openPrivate(path)
	if err != nil {
		return Config{}, err
	}
	defer func() { _ = f.Close() }()
	return decode(f, path, tailscaleIPs)
}

// Create writes the first owner-only fleet config without replacing an
// existing operator policy.
func Create(path string, cfg Config, tailscaleIPs TailscaleIPs) error {
	if err := cfg.validate(tailscaleIPs); err != nil {
		return err
	}
	if err := fssecure.EnsureConfigDir(filepath.Dir(path)); err != nil {
		return err
	}
	unlock, err := lockConfig(path)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("fleet config already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return replacePrivate(path, body)
}

func decode(r io.Reader, path string, tailscaleIPs TailscaleIPs) (Config, error) {
	cfg := Config{Local: Local{MaxConcurrent: 1, Contribution: "50%,50%"}}
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple YAML documents are not allowed")
		}
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.validate(tailscaleIPs); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// AppendExecutor serializes Sparkwing enrollment writes and refuses a config
// edit observed while the new enrollment is prepared.
func AppendExecutor(path string, executor Executor, tailscaleIPs TailscaleIPs) (Config, error) {
	return AppendExecutorPrepared(path, executor, tailscaleIPs, nil)
}

// ExecutorPreparation performs private side effects after policy validation.
// Its rollback runs when preparation or the following config commit fails.
type ExecutorPreparation func(Config) (rollback func() error, err error)

// AppendExecutorPrepared serializes policy validation, credential preparation,
// and the config commit so concurrent enrollments cannot rotate each other.
func AppendExecutorPrepared(path string, executor Executor, tailscaleIPs TailscaleIPs, prepare ExecutorPreparation) (result Config, retErr error) {
	unlock, err := lockConfig(path)
	if err != nil {
		return Config{}, err
	}
	defer unlock()
	f, err := openPrivate(path)
	if err != nil {
		return Config{}, err
	}
	original, err := io.ReadAll(f)
	closeErr := f.Close()
	if err != nil {
		return Config{}, err
	}
	if closeErr != nil {
		return Config{}, closeErr
	}
	cfg, err := decode(bytes.NewReader(original), path, tailscaleIPs)
	if err != nil {
		return Config{}, err
	}
	for _, existing := range cfg.Executors {
		if existing.Name == executor.Name {
			return Config{}, fmt.Errorf("executor %q is already enrolled", executor.Name)
		}
	}
	cfg.Executors = append(cfg.Executors, executor)
	if err := cfg.validate(tailscaleIPs); err != nil {
		return Config{}, err
	}
	var rollback func() error
	if prepare != nil {
		rollback, err = prepare(cfg)
		if err != nil {
			if rollback != nil {
				if rollbackErr := rollback(); rollbackErr != nil {
					err = errors.Join(err, fmt.Errorf("rollback executor preparation: %w", rollbackErr))
				}
			}
			return Config{}, err
		}
		defer func() {
			if retErr != nil && rollback != nil {
				if rollbackErr := rollback(); rollbackErr != nil {
					retErr = errors.Join(retErr, fmt.Errorf("rollback executor preparation: %w", rollbackErr))
				}
			}
		}()
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return Config{}, err
	}
	current, err := readPrivate(path)
	if err != nil {
		return Config{}, err
	}
	if !bytes.Equal(current, original) {
		return Config{}, errors.New("fleet config changed while enrollment was prepared")
	}
	if err := replacePrivate(path, body); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func readPrivate(path string) ([]byte, error) {
	f, err := openPrivate(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

func lockConfig(path string) (func(), error) {
	lockPath := path + ".lock"
	for range 5 {
		info, err := os.Lstat(lockPath)
		if errors.Is(err, os.ErrNotExist) {
			f, createErr := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
			if errors.Is(createErr, os.ErrExist) {
				continue
			}
			if createErr != nil {
				return nil, createErr
			}
			if err := securePrivateFile(lockPath); err != nil {
				_ = f.Close()
				return nil, err
			}
			locked, lockErr := flockTry(f)
			if lockErr != nil || !locked {
				_ = f.Close()
				if lockErr != nil {
					return nil, lockErr
				}
				return nil, errors.New("fleet config is busy")
			}
			return func() { _ = flockUnlock(f); _ = f.Close() }, nil
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("fleet config lock must be a regular file")
		}
		if err := verifyPrivateFile(lockPath, info); err != nil {
			return nil, fmt.Errorf("fleet config lock is not owner-only: %w", err)
		}
		f, err := os.OpenFile(lockPath, os.O_RDWR, 0)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		opened, statErr := f.Stat()
		if statErr != nil || !os.SameFile(info, opened) {
			_ = f.Close()
			if statErr != nil {
				return nil, statErr
			}
			continue
		}
		locked, lockErr := flockTry(f)
		if lockErr != nil || !locked {
			_ = f.Close()
			if lockErr != nil {
				return nil, lockErr
			}
			return nil, errors.New("fleet config is busy")
		}
		return func() { _ = flockUnlock(f); _ = f.Close() }, nil
	}
	return nil, errors.New("fleet config lock kept changing")
}

func replacePrivate(path string, body []byte) (retErr error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".fleet-*.yaml")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if err := securePrivateFile(tmpPath); err != nil {
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return nil
}

func openPrivate(path string) (*os.File, error) {
	f, err := fssecure.OpenPrivateConfig(path)
	if err != nil {
		return nil, fmt.Errorf("read fleet config: %w", err)
	}
	return f, nil
}

func (c *Config) validate(tailscaleIPs TailscaleIPs) error {
	listenHost, listenPort, err := net.SplitHostPort(strings.TrimSpace(c.Listen))
	if err != nil || listenPort == "" || listenPort == "0" {
		return errors.New("listen must be a fixed host:port with a nonzero port")
	}
	if listenHost == "" || listenHost == "0.0.0.0" || listenHost == "::" {
		return errors.New("listen must name one literal local address, not a wildcard")
	}
	if _, ok := parseIP(listenHost); !ok {
		return errors.New("listen must use a literal local IP address")
	}
	port, err := strconv.Atoi(listenPort)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("listen port must be between 1 and 65535")
	}
	public, err := url.Parse(strings.TrimSpace(c.PublicURL))
	if err != nil || public.Scheme == "" || public.Host == "" || public.User != nil || public.RawQuery != "" || public.Fragment != "" || (public.Path != "" && public.Path != "/") {
		return errors.New("public_url must be an origin URL without credentials, path, query, or fragment")
	}
	if public.Scheme != "http" && public.Scheme != "https" {
		return errors.New("public_url scheme must be http or https")
	}
	publicPort := public.Port()
	if publicPort == "" {
		if public.Scheme == "https" {
			publicPort = "443"
		} else {
			publicPort = "80"
		}
	}
	if public.Scheme == "https" {
		if !isLoopbackHost(listenHost) {
			return errors.New("https public_url requires a literal loopback listen address for the local proxy")
		}
	} else {
		if publicPort != listenPort {
			return errors.New("http public_url port must match listen")
		}
		if !sameHost(listenHost, public.Hostname()) {
			return errors.New("http public_url host must match listen")
		}
	}
	publicIP, publicIsIP := parseIP(public.Hostname())
	listenIP, listenIsIP := parseIP(listenHost)
	publicLoopback := publicIsIP && publicIP.IsLoopback() || strings.EqualFold(public.Hostname(), "localhost")
	if c.AllowTailnetHTTP && (public.Scheme != "http" || publicLoopback) {
		return errors.New("allow_tailnet_http applies only to a non-loopback HTTP public_url")
	}
	if public.Scheme == "http" && !publicLoopback {
		if !c.AllowTailnetHTTP {
			return errors.New("non-loopback http public_url requires allow_tailnet_http: true")
		}
		if !publicIsIP || !listenIsIP || publicIP != listenIP {
			return errors.New("plain HTTP requires identical literal public_url and listen IPs")
		}
		if tailscaleIPs == nil {
			return errors.New("plain HTTP requires local Tailscale IP verification")
		}
		ips, err := tailscaleIPs()
		if err != nil {
			return fmt.Errorf("verify local Tailscale IP: %w", err)
		}
		matched := false
		for _, ip := range ips {
			matched = matched || ip == publicIP
		}
		if !matched {
			return fmt.Errorf("plain HTTP public IP %s is not a local Tailscale IP", publicIP)
		}
	}
	c.Listen = net.JoinHostPort(listenHost, listenPort)
	public.Path = ""
	c.PublicURL = strings.TrimRight(public.String(), "/")
	if c.Local.Name == "" {
		c.Local.Name = "local"
	}
	if !executorNamePattern.MatchString(c.Local.Name) {
		return errors.New("local.name must be 1-64 letters, digits, dots, underscores, or hyphens and start with a letter or digit")
	}
	if c.Local.MaxConcurrent < 1 {
		return errors.New("local.max_concurrent must be at least 1")
	}
	contribution, err := wingd.ParseBudget(c.Local.Contribution)
	if err != nil {
		return fmt.Errorf("local.contribution: %w", err)
	}
	if !contribution.HasCap() || contribution.Enforce || contribution.IgnoreExternal {
		return errors.New("local.contribution must set a CPU or memory cap without admission modifiers")
	}
	reserve, err := wingd.ParseBudget(c.Local.LocalReserve)
	if err != nil {
		return fmt.Errorf("local.local_reserve: %w", err)
	}
	if reserve.Enforce || reserve.IgnoreExternal {
		return errors.New("local.local_reserve accepts only CPU or memory capacity")
	}
	c.Local.Capabilities = cleanStrings(c.Local.Capabilities)
	if err := rejectReservedCapabilities("local.capabilities", c.Local.Capabilities); err != nil {
		return err
	}
	return c.validateExecutors()
}

func (c *Config) validateExecutors() error {
	seenNames := map[string]bool{c.Local.Name: true}
	for i := range c.Executors {
		e := &c.Executors[i]
		e.Name = strings.TrimSpace(e.Name)
		if !executorNamePattern.MatchString(e.Name) || seenNames[e.Name] {
			return fmt.Errorf("executors[%d].name must be unique and use 1-64 letters, digits, dots, underscores, or hyphens", i)
		}
		seenNames[e.Name] = true
		e.Capabilities = cleanStrings(e.Capabilities)
		e.Location = strings.TrimSpace(e.Location)
		if e.Location != "local" && e.Location != "cloud" {
			return fmt.Errorf("executors[%d].location must be local or cloud", i)
		}
		if err := rejectReservedCapabilities(fmt.Sprintf("executors[%d].capabilities", i), e.Capabilities); err != nil {
			return err
		}
		if e.BasePriority < 0 || e.PriorityCeiling < e.BasePriority || e.PriorityCeiling > 100 || e.MaxConcurrent < 1 || e.Budget.Cores < 0 || math.IsNaN(e.Budget.Cores) || math.IsInf(e.Budget.Cores, 0) || e.Budget.MemoryBytes < 0 {
			return fmt.Errorf("executors[%d] has invalid priority or contribution limits", i)
		}
	}
	return nil
}

func rejectReservedCapabilities(field string, values []string) error {
	for _, value := range values {
		lower := strings.ToLower(value)
		for _, prefix := range []string{"os=", "arch=", "environment=", "location="} {
			if strings.HasPrefix(lower, prefix) {
				return fmt.Errorf("%s cannot set reserved machine or placement key %q", field, strings.TrimSuffix(prefix, "="))
			}
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	ip, ok := parseIP(host)
	return ok && ip.IsLoopback()
}

func cleanStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, value := range in {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func sameHost(a, b string) bool {
	aIP, aOK := parseIP(a)
	bIP, bOK := parseIP(b)
	if aOK || bOK {
		return aOK && bOK && aIP == bIP
	}
	return strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, "."))
}

func parseIP(value string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(strings.Trim(value, "[]"))
	return ip.Unmap(), err == nil
}

func (e Executor) Registration(principal string) store.Executor {
	return store.Executor{
		Name: e.Name, Kind: "agent", Location: e.Location,
		Capabilities: e.Capabilities, BasePriority: e.BasePriority,
		PriorityCeiling: e.PriorityCeiling, MaxConcurrent: e.MaxConcurrent,
		Budget: e.Budget, Principal: principal,
	}
}

func ResolvePath(path string) (string, error) {
	if path != "" {
		return filepath.Abs(path)
	}
	return DefaultPath()
}
