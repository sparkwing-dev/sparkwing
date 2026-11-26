package jobs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"go.yaml.in/yaml/v3"
)

const deploySettingsPath = "sparkwing-deploy/deploy.yaml"

// safety: the role and region sign in before deploy.yaml is readable, so they and the configuration
// repository come from the runner's machine settings. No name has a KEY, TOKEN or SECRET segment,
// because the engine withholds credential-shaped variables from pipeline jobs.
const (
	pipelinesEnvConfigRepo  = "SPARKWING_PIPELINES_DEPLOY_CONFIG_REPO"
	pipelinesEnvConfigParam = "SPARKWING_PIPELINES_DEPLOY_CONFIG_PARAMETER"
	pipelinesEnvDeployRole  = "SPARKWING_PIPELINES_DEPLOY_ROLE"
	pipelinesEnvDeployReg   = "SPARKWING_PIPELINES_DEPLOY_REGION"
	pipelinesEnvOwnedOwners = "SPARKWING_PIPELINES_OWNED_SDK_OWNERS"
)

func pipelinesEnvFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sparkwing-pipelines", "env"), nil
}

// safety: the environment wins even when present and empty, so a one-off override beats a stale file.
func pipelinesSetting(key string) (string, error) {
	if v, ok := os.LookupEnv(key); ok {
		return v, nil
	}
	path, err := pipelinesEnvFile()
	if err != nil {
		return "", err
	}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	value := ""
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		value = v
	}
	return value, sc.Err()
}

type deployBootstrap struct {
	ConfigRepo string
	ConfigKey  string
	RoleARN    string
	Region     string
}

var (
	roleARNRE = regexp.MustCompile(`^arn:aws[a-z-]*:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]+$`)
	regionRE  = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)
)

func readDeployBootstrap() (deployBootstrap, error) {
	var b deployBootstrap
	var missing []string
	for _, s := range []struct {
		key string
		dst *string
	}{
		{pipelinesEnvConfigRepo, &b.ConfigRepo},
		{pipelinesEnvConfigParam, &b.ConfigKey},
		{pipelinesEnvDeployRole, &b.RoleARN},
		{pipelinesEnvDeployReg, &b.Region},
	} {
		v, err := pipelinesSetting(s.key)
		if err != nil {
			return b, err
		}
		if *s.dst = strings.TrimSpace(v); *s.dst == "" {
			missing = append(missing, s.key)
		}
	}
	file, _ := pipelinesEnvFile()
	if len(missing) > 0 {
		return b, fmt.Errorf("the deploy runner's machine settings lack %s; set them in %s or the environment as DELIVERY.md's \"Deploy configuration\" section describes", strings.Join(missing, ", "), file)
	}
	var errs []error
	if !roleARNRE.MatchString(b.RoleARN) {
		errs = append(errs, fmt.Errorf("%s %q is not an IAM role ARN", pipelinesEnvDeployRole, b.RoleARN))
	}
	if !regionRE.MatchString(b.Region) {
		errs = append(errs, fmt.Errorf("%s %q is not an AWS region", pipelinesEnvDeployReg, b.Region))
	}
	if !strings.HasPrefix(b.ConfigKey, "/") {
		errs = append(errs, fmt.Errorf("%s %q is not a parameter path", pipelinesEnvConfigParam, b.ConfigKey))
	}
	return b, errors.Join(errs...)
}

// safety: unknown keys are refused so a misspelled key fails instead of leaving a value unset.
type deploySettings struct {
	Registry string `yaml:"registry"`
	Cluster  string `yaml:"cluster"`
	Bases    struct {
		Controller string `yaml:"controller"`
		Runner     string `yaml:"runner"`
	} `yaml:"bases"`
	Repositories struct {
		Gitops    string `yaml:"gitops"`
		Reference string `yaml:"reference"`
	} `yaml:"repositories"`
	Parameters struct {
		GitopsKey        string `yaml:"gitops_key"`
		ReferenceKey     string `yaml:"reference_key"`
		StagingGateToken string `yaml:"staging_gate_token"`
		SmokeSubmitToken string `yaml:"smoke_submit_token"`
	} `yaml:"parameters"`
	Environments struct {
		Staging    deployEnvSettings `yaml:"staging"`
		Production deployEnvSettings `yaml:"production"`
	} `yaml:"environments"`
}

type deployEnvSettings struct {
	GitopsDir            string `yaml:"gitops_dir"`
	HelmRelease          string `yaml:"helm_release"`
	ArgoApp              string `yaml:"argo_app"`
	ControllerDeployment string `yaml:"controller_deployment"`
	LauncherDeployment   string `yaml:"launcher_deployment"`
	PrepullTag           string `yaml:"prepull_tag"`
}

func parseDeploySettings(source string, body []byte) (deploySettings, error) {
	var s deploySettings
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return s, fmt.Errorf("deploy settings %s: %w", source, err)
	}
	if err := s.validate(); err != nil {
		return s, fmt.Errorf("deploy settings %s: %w", source, err)
	}
	return s, nil
}

func (s deploySettings) validate() error {
	var missing []string
	need := func(key, value string) {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, key)
		}
	}
	need("registry", s.Registry)
	need("cluster", s.Cluster)
	need("bases.controller", s.Bases.Controller)
	need("bases.runner", s.Bases.Runner)
	need("repositories.gitops", s.Repositories.Gitops)
	need("repositories.reference", s.Repositories.Reference)
	need("parameters.gitops_key", s.Parameters.GitopsKey)
	need("parameters.reference_key", s.Parameters.ReferenceKey)
	need("parameters.staging_gate_token", s.Parameters.StagingGateToken)
	need("parameters.smoke_submit_token", s.Parameters.SmokeSubmitToken)
	for _, env := range []struct {
		name string
		s    deployEnvSettings
	}{{"staging", s.Environments.Staging}, {"production", s.Environments.Production}} {
		prefix := "environments." + env.name + "."
		need(prefix+"gitops_dir", env.s.GitopsDir)
		need(prefix+"helm_release", env.s.HelmRelease)
		need(prefix+"argo_app", env.s.ArgoApp)
		need(prefix+"controller_deployment", env.s.ControllerDeployment)
		need(prefix+"launcher_deployment", env.s.LauncherDeployment)
		need(prefix+"prepull_tag", env.s.PrepullTag)
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing %s", strings.Join(missing, ", "))
	}
	var errs []error
	for key, base := range map[string]string{"bases.controller": s.Bases.Controller, "bases.runner": s.Bases.Runner} {
		// safety: a base by tag could move under the deploy; only a digest
		// in this deploy's registry is a pin.
		if _, err := name.NewDigest(base); err != nil || !strings.HasPrefix(base, s.Registry+"/") {
			errs = append(errs, fmt.Errorf("%s %q must be an image in %s by digest", key, base, s.Registry))
		}
	}
	for key, param := range map[string]string{
		"parameters.gitops_key": s.Parameters.GitopsKey, "parameters.reference_key": s.Parameters.ReferenceKey,
		"parameters.staging_gate_token": s.Parameters.StagingGateToken, "parameters.smoke_submit_token": s.Parameters.SmokeSubmitToken,
	} {
		if !strings.HasPrefix(param, "/") {
			errs = append(errs, fmt.Errorf("%s %q is not a parameter path", key, param))
		}
	}
	if s.Environments.Staging.GitopsDir == s.Environments.Production.GitopsDir {
		errs = append(errs, errors.New("staging and production share a gitops_dir"))
	}
	return errors.Join(errs...)
}

func (c *deployConfig) applySettings(s deploySettings) {
	c.Registry, c.Cluster = s.Registry, s.Cluster
	if c.ControllerBase == "" {
		c.ControllerBase = s.Bases.Controller
	}
	if c.RunnerBase == "" {
		c.RunnerBase = s.Bases.Runner
	}
	c.GitopsURL, c.ReferenceURL = s.Repositories.Gitops, s.Repositories.Reference
	c.Params = deployParams{
		GitopsKey:    s.Parameters.GitopsKey,
		ReferenceKey: s.Parameters.ReferenceKey,
		StagingToken: s.Parameters.StagingGateToken,
		SmokeToken:   s.Parameters.SmokeSubmitToken,
	}
	for _, e := range []struct {
		env *deployEnv
		s   deployEnvSettings
	}{{&c.Staging, s.Environments.Staging}, {&c.Production, s.Environments.Production}} {
		e.env.GitopsDir, e.env.HelmRelease, e.env.ArgoApp = e.s.GitopsDir, e.s.HelmRelease, e.s.ArgoApp
		e.env.ControllerDeployment, e.env.LauncherDeployment, e.env.PrepullTag = e.s.ControllerDeployment, e.s.LauncherDeployment, e.s.PrepullTag
	}
}

// safety: a caller may hold an environment captured before the settings loaded, so look it up by name.
func (c deployConfig) environment(name string) deployEnv {
	if name == c.Production.Name {
		return c.Production
	}
	return c.Staging
}

// safety: the settings load at the first sign-in, so superseded commits stop before any credential.
func (d *deployer) signIn(ctx context.Context) error {
	if d.settled {
		return d.assumeRole(ctx)
	}
	boot, err := readDeployBootstrap()
	if err != nil {
		return err
	}
	d.cfg.RoleARN, d.cfg.Region = boot.RoleARN, boot.Region
	if err := d.assumeRole(ctx); err != nil {
		return err
	}
	var body []byte
	source := d.settingsFile
	if d.dry {
		if source == "" {
			return errors.New("a dry run cannot clone the deploy configuration repository; pass --deploy-config with a local copy of " + deploySettingsPath)
		}
		d.tools.say(ctx, "dry-run: would clone "+boot.ConfigRepo+" with the key in "+boot.ConfigKey+" and read "+deploySettingsPath+"; reading "+source+" instead")
		if body, err = os.ReadFile(source); err != nil {
			return err
		}
	} else {
		source = boot.ConfigRepo + " " + deploySettingsPath
		if body, err = d.cloneSettings(ctx, boot); err != nil {
			return err
		}
	}
	settings, err := parseDeploySettings(source, body)
	if err != nil {
		return err
	}
	d.cfg.applySettings(settings)
	d.settled = true
	return nil
}

func (d *deployer) cloneSettings(ctx context.Context, boot deployBootstrap) ([]byte, error) {
	var body []byte
	err := d.act(ctx, "read "+deploySettingsPath+" from "+boot.ConfigRepo, func() error {
		env, err := d.gitEnv(ctx, boot.ConfigKey, "config-key")
		if err != nil {
			return err
		}
		dir := filepath.Join(d.tmp, "deploy-config")
		if _, err := d.git(ctx, env, "", "clone", "--quiet", "--depth", "1", boot.ConfigRepo, dir); err != nil {
			return err
		}
		body, err = os.ReadFile(filepath.Join(dir, deploySettingsPath))
		return err
	})
	return body, err
}
