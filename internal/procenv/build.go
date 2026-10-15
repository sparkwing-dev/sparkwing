package procenv

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/envredact"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// Lookup uses exact rows before prefixes, case-insensitively on Windows; unknown names remain user-owned.
func Lookup(name string) (Var, bool) {
	key := envKey(name)
	for _, row := range Registry {
		if !row.Prefix && envKey(row.Name) == key {
			return row, true
		}
	}
	var match Var
	for _, row := range Registry {
		if row.Prefix && strings.HasPrefix(key, envKey(row.Name)) && len(row.Name) > len(match.Name) {
			match = row
		}
	}
	if match.Name != "" {
		return match, true
	}
	return Var{Name: name, Class: User, Owner: "user"}, false
}

func envKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

// Validate refuses invalid names and engine-owned assignments without exposing values in errors.
func Validate(env []Assignment) error {
	for _, assignment := range env {
		if !validName(assignment.Name) {
			return errors.New("invalid environment variable name")
		}
		if strings.ContainsRune(assignment.Value, 0) {
			return fmt.Errorf("environment variable %q contains NUL", assignment.Name)
		}
		row, _ := Lookup(assignment.Name)
		if row.Owner == "engine" || credentialName(row, assignment.Name) {
			return fmt.Errorf("environment variable %q is reserved or credential-shaped", assignment.Name)
		}
	}
	return nil
}

func validName(name string) bool { return name != "" && !strings.ContainsAny(name, "=\x00") }

// Build applies the process policy before execution. Local input comes only from Captured;
// remote input comes from Ambient and the explicit allow list. Masker must be supplied when
// accepted remote values or embedded proxy credentials require registration.
func Build(k Kind, b Base, rt Runtime) ([]string, error) {
	if k > HostChild {
		return nil, errors.New("unknown process environment kind")
	}
	if k == StepTool || k == Cleanup {
		return inheritAttempt(rt.AttemptEnv, b.Masker)
	}
	if !b.Local && (k == Plan || k == Attempt || k == Eval || k == Install) {
		if err := ValidateSubmitEnvAllow(b.SubmitEnvAllow); err != nil {
			return nil, err
		}
	}
	env := make(map[string]string)
	input := b.Ambient
	if b.Local || k == HostChild {
		input = b.Captured
	}
	for _, entry := range input {
		name, value, err := parseEntry(entry)
		if err != nil {
			return nil, err
		}
		row, known := Lookup(name)
		if !inherited(k, row, known, name, b) {
			continue
		}
		if err := registerMasks(b.Masker, name, value, !b.Local && k != HostChild); err != nil {
			return nil, err
		}
		env[envKey(name)] = name + "=" + value
	}
	if k == Plan || k == Attempt || k == Eval || k == RunHost || k == NodeHost {
		composeRuntime(env, k, rt)
	}
	if k == Attempt || k == Eval {
		if err := Validate(rt.Startup); err != nil {
			return nil, err
		}
		for _, a := range rt.Startup {
			if err := registerMasks(b.Masker, a.Name, a.Value, !b.Local); err != nil {
				return nil, err
			}
			env[envKey(a.Name)] = a.Name + "=" + a.Value
		}
	}
	result := make([]string, 0, len(env))
	for _, entry := range env {
		if _, _, err := parseEntry(entry); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	slices.Sort(result)
	return result, nil
}

func parseEntry(entry string) (string, string, error) {
	name, value, ok := strings.Cut(entry, "=")
	if !ok || !validName(name) || strings.ContainsRune(value, 0) {
		return "", "", errors.New("invalid environment assignment")
	}
	return name, value, nil
}

func credentialName(row Var, name string) bool {
	// safety: the registry's exact CA-file entries are public paths, not credential values.
	if row.Class == Tool && row.RemoteBase && !row.Prefix {
		return false
	}
	return row.Class == Credential || envredact.CredentialName(name)
}

func inherited(k Kind, row Var, known bool, name string, b Base) bool {
	if k == CLI && envKey(name) == envKey("SPARKWING_SECRETS_KEY_FILE") {
		return true
	}
	if credentialName(row, name) {
		return false
	}
	if row.Owner == "engine" {
		return k == CLI && (row.Name == "SPARKWING_TOOLCHAIN_ACTIVE" || row.Name == "SPARKWING_HOOK_SELFTEST" || row.Name == "SPARKWING_TESTLEAK_HOST" || row.Name == "SPARKWING_TEST_")
	}
	switch k {
	case CLI:
		return row.Class == Tool || (known && (row.Class == User || row.Class == Diagnostic))
	case DaemonSpawn:
		return row.RemoteBase || strings.HasPrefix(name, "XDG_")
	case Controller, Runner, Launcher:
		return row.Class == Tool
	case RunHost, NodeHost:
		return false
	case JobContainer:
		return row.RemoteBase || name == "GOCACHE" || name == "GOMODCACHE" || name == "npm_config_cache" || name == "XDG_CACHE_HOME"
	case HostChild:
		return row.Class != Diagnostic
	case Plan, Attempt, Eval, Install:
		if k == Install && row.Class == Diagnostic {
			return false
		}
		if b.Local {
			return true
		}
		if row.RemoteBase {
			return true
		}
		for _, allowed := range b.SubmitEnvAllow {
			if envKey(allowed) == envKey(name) || (strings.HasSuffix(allowed, "_*") && strings.HasPrefix(envKey(name), envKey(strings.TrimSuffix(allowed, "*")))) {
				return true
			}
		}
	}
	return false
}

func composeRuntime(env map[string]string, k Kind, rt Runtime) {
	put := func(name, value string) {
		if value != "" {
			env[envKey(name)] = name + "=" + value
		}
	}
	put("SPARKWING_RUN_ID", rt.RunID)
	if k != Plan {
		put("SPARKWING_JOB_ID", rt.JobID)
	}
	put("SPARKWING_RUNNER_NAME", rt.Runner.Name)
	put("SPARKWING_RUNNER_TYPE", rt.Runner.Type)
	if len(rt.Runner.Labels) > 0 {
		put("SPARKWING_RUNNER_LABELS", strings.Join(rt.Runner.Labels, ","))
	}
	put("SPARKWING_DRY_RUN", strconv.FormatBool(rt.DryRun))
	put("CI", "true")
	put("SPARKWING_BRANCH", rt.Branch)
	put("SPARKWING_COMMIT", rt.Commit)
	put("SPARKWING_REPO", rt.Repo)
	put("SPARKWING_TRIGGER_SOURCE", rt.TriggerSource)
	if rt.GateIndex != nil {
		put("SPARKWING_GATE_INDEX", strconv.Itoa(*rt.GateIndex))
	}
}

func registerMasks(masker *secrets.Masker, name, value string, remote bool) error {
	values := []string{}
	if remote && len(value) >= limits.OperatorEnvMaskMinLength {
		values = append(values, value)
	}
	for _, address := range proxyAddresses(name, value) {
		if !strings.Contains(address, "://") {
			address = "http://" + strings.TrimPrefix(address, "//")
		}
		u, err := url.Parse(address)
		if err != nil {
			return fmt.Errorf("invalid proxy URL in %q", name)
		}
		if u.User != nil {
			values = append(values, u.User.String(), u.User.Username())
			if password, ok := u.User.Password(); ok {
				values = append(values, password)
			}
			// safety: consumers may log the original escaped spelling as well as decoded credentials.
			_, authority, _ := strings.Cut(address, "://")
			authority = strings.FieldsFunc(authority, func(r rune) bool { return r == '/' || r == '?' || r == '#' })[0]
			userinfo := authority[:strings.LastIndex(authority, "@")]
			username, password, _ := strings.Cut(userinfo, ":")
			values = append(values, userinfo, username, password)
		}
	}
	for _, value := range values {
		if value == "" {
			continue
		}
		if masker == nil {
			return errors.New("environment requires a secret masker")
		}
		masker.Register(value)
	}
	return nil
}

func proxyAddresses(name, value string) []string {
	switch strings.ToUpper(name) {
	case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "PIP_INDEX_URL", "PIP_PROXY", "NPM_CONFIG_REGISTRY", "NPM_CONFIG_PROXY", "NPM_CONFIG_HTTPS_PROXY":
		return []string{value}
	case "GOPROXY":
		return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '|' })
	case "PIP_EXTRA_INDEX_URL":
		return strings.Fields(value)
	default:
		return nil
	}
}

func inheritAttempt(env []string, masker *secrets.Masker) ([]string, error) {
	for _, entry := range env {
		name, value, err := parseEntry(entry)
		if err != nil {
			return nil, err
		}
		row, _ := Lookup(name)
		if credentialName(row, name) || row.Class == Protocol || (row.Owner == "engine" && row.Class != RuntimeClass) {
			return nil, fmt.Errorf("attempt environment contains reserved variable %q", name)
		}
		if err := registerMasks(masker, name, value, false); err != nil {
			return nil, err
		}
	}
	return slices.Clone(env), nil
}

var submitEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateSubmitEnvAllow accepts literal names and terminal _* prefix patterns.
// Credentials remain excluded by Build even when a pattern would match them.
func ValidateSubmitEnvAllow(patterns []string) error {
	for _, pattern := range patterns {
		literal := pattern
		if strings.HasSuffix(pattern, "_*") {
			literal = strings.TrimSuffix(pattern, "*")
		}
		if !submitEnvName.MatchString(literal) {
			return errors.New("run.submit_env_allow requires literal environment names or terminal _* prefixes")
		}
	}
	return nil
}
