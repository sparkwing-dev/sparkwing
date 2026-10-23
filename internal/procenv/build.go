package procenv

import (
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/envredact"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
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
	return Var{Name: name, Class: user, Owner: "user"}, false
}

func envKey(name string) string { return NameKey(name, runtime.GOOS) }

// NameKey returns the platform's environment-name comparison key.
func NameKey(name, goos string) string {
	if goos == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

func validate(env []Assignment) error {
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
// Build excludes credential-class values from both local snapshots and remote inputs at every boundary.
// This includes tool-owned credentials in a local snapshot. Plan never receives job Startup;
// Attempt and Eval receive it before process start. The SDK adds Body only after plan succeeds.
// Cleanup and StepTool use AttemptEnv exactly. Remote allow-list values of at least twelve characters
// are masked before any subprocess starts. Build drops envredact.CredentialName matches before
// applying allow lists, even when Lookup classifies an unknown name as user-owned.
// Git also drops inherited GIT_* except GIT_SSL_CAINFO and GIT_SSL_CAPATH, plus
// SSH_ASKPASS and SSH_AUTH_SOCK. Source acquisition selects private credential/config files through argv.
// Git never inherits a repository/index/config override or a credential helper from a caller.
// Remote installs on a host the run's owner does not own set GOENV=off.
func Build(k Kind, b Base, rt Runtime) (Env, error) {
	values, err := build(k, b, rt)
	return Env{values: values, masker: b.Masker, remote: !b.Local && k != HostChild}, err
}

func build(k Kind, b Base, rt Runtime) ([]string, error) {
	if k >= kindCount {
		return nil, errors.New("unknown process environment kind")
	}
	if k == StepTool || k == Cleanup {
		return inheritAttempt(rt.AttemptEnv.values, b.Masker)
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
		if err := registerMasks(b.Masker, name, value, !b.Local && k != HostChild && len(value) >= limits.OperatorEnvMaskMinLength); err != nil {
			return nil, err
		}
		env[envKey(name)] = name + "=" + value
	}
	if k == Plan || k == Attempt || k == Eval || k == runHost || k == nodeHost {
		composeRuntime(env, k, rt)
	}
	if k == Attempt || k == Eval {
		if err := validate(rt.Startup); err != nil {
			return nil, err
		}
		for _, a := range rt.Startup {
			if err := registerMasks(b.Masker, a.Name, a.Value, !b.Local && len(a.Value) >= limits.OperatorEnvMaskMinLength); err != nil {
				return nil, err
			}
			env[envKey(a.Name)] = a.Name + "=" + a.Value
		}
	}
	if k == Git {
		env[envKey("GIT_NO_REPLACE_OBJECTS")] = "GIT_NO_REPLACE_OBJECTS=1"
	}
	// safety: a shared host's go env file is writable by earlier jobs, so remote compiles ignore it.
	if k == Install && !b.Local && !b.OwnerMachine {
		env[envKey("GOENV")] = "GOENV=off"
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
	if row.Class == tool && row.RemoteBase && !row.Prefix {
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
		return k == CLI && (row.Name == "SPARKWING_TOOLCHAIN_ACTIVE" || row.Name == "SPARKWING_TESTLEAK_HOST" || row.Name == "SPARKWING_TEST_")
	}
	if k == Git {
		key := strings.ToUpper(name)
		if strings.HasPrefix(key, "GIT_") && key != "GIT_SSL_CAINFO" && key != "GIT_SSL_CAPATH" || key == "SSH_ASKPASS" || key == "SSH_AUTH_SOCK" {
			return false
		}
		return row.RemoteBase
	}
	switch k {
	case CLI:
		return row.Class == tool || (known && (row.Class == user || row.Class == diagnostic))
	case daemonSpawn:
		return row.RemoteBase || strings.HasPrefix(name, "XDG_")
	case controller, runner, launcher:
		return row.Class == tool
	case runHost, nodeHost:
		return false
	case jobContainer:
		return row.RemoteBase || name == "GOCACHE" || name == "GOMODCACHE" || name == "npm_config_cache" || name == "XDG_CACHE_HOME"
	case HostChild:
		return row.Class != diagnostic
	case Plan, Attempt, Eval, Install:
		if k == Install && row.Class == diagnostic {
			return false
		}
		if b.Local || ownerTool(b, row) {
			return true
		}
		if row.RemoteBase {
			return true
		}
		if slices.ContainsFunc(b.Submitted, func(submitted string) bool { return envKey(submitted) == envKey(name) }) {
			return k != Install
		}
		return submitAllowed(name, b.SubmitEnvAllow)
	}
	return false
}

func ownerTool(b Base, row Var) bool { return b.OwnerMachine && row.GoTool }

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
	put("SPARKWING_GATE_INDEX", rt.GateIndex)
}

func registerMasks(masker *secrets.Masker, name, value string, maskValue bool) error {
	values := []string{}
	// Plain config remains usable even when it cannot be a masking pattern.
	if maskValue && secrets.Validate(value) == nil {
		values = append(values, value)
	}
	credentials, err := proxyCredentials(name, value)
	if err != nil {
		return err
	}
	values = append(values, credentials...)
	for _, value := range values {
		if secrets.Validate(value) != nil {
			continue
		}
		if masker == nil {
			return errors.New("environment requires a secret masker")
		}
		if err := masker.Register(value); err != nil {
			return err
		}
	}
	return nil
}

func inheritAttempt(env []string, masker *secrets.Masker) ([]string, error) {
	for _, entry := range env {
		name, value, err := parseEntry(entry)
		if err != nil {
			return nil, err
		}
		row, _ := Lookup(name)
		if credentialName(row, name) || row.Class == Protocol || (row.Owner == "engine" && row.Class != runtimeClass) {
			return nil, fmt.Errorf("attempt environment contains reserved variable %q", name)
		}
		if err := registerMasks(masker, name, value, false); err != nil {
			return nil, err
		}
	}
	return slices.Clone(env), nil
}

var (
	submitEnvName   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	submitEnvPrefix = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]+_\*$`)
)

// ValidateSubmitEnvAllow accepts literal names and terminal _* patterns whose
// prefix starts with a letter and has at least two characters.
// Credentials remain excluded by Build even when a pattern would match them.
func ValidateSubmitEnvAllow(patterns []string) error {
	for _, pattern := range patterns {
		if !strings.HasSuffix(pattern, "_*") {
			if !submitEnvName.MatchString(pattern) {
				return errors.New("run.submit_env_allow requires literal environment names or terminal _* prefixes")
			}
			continue
		}
		if !submitEnvName.MatchString(strings.TrimSuffix(pattern, "*")) {
			return errors.New("run.submit_env_allow requires literal environment names or terminal _* prefixes")
		}
		if !submitEnvPrefix.MatchString(pattern) {
			return fmt.Errorf("run.submit_env_allow pattern %q is too broad: name a prefix of at least two characters starting with a letter, such as BUILD_*", pattern)
		}
	}
	return nil
}
