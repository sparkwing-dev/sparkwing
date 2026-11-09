package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/pkg/cachecontrol"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	deployPhaseController = "controller"
	deployPhaseLauncher   = "launcher"
	deployDryDigest       = "sha256:<digest-from-build>"
)

type deployCommand struct {
	Dir   string
	Env   map[string]string
	Name  string
	Args  []string
	Stdin string
	Quiet bool
}

func (c deployCommand) String() string {
	return strings.Join(append([]string{c.Name}, c.Args...), " ")
}

type deployRegistry interface {
	retain(ctx context.Context, password string, pin imagePin) error
	digest(ctx context.Context, password, ref string) (string, error)
	revision(ctx context.Context, password, ref string) (string, error)
	publish(ctx context.Context, password string, in PublishImagesArgs) error
}

type deployTools struct {
	awaitChild func(context.Context, string) (string, error)
	startChild func(context.Context, string, map[string]string) (string, error)
	run        func(context.Context, deployCommand) (string, error)
	registry   deployRegistry
	oidc       func(context.Context, string) (string, error)
	wait       func(context.Context, time.Duration) error
	now        func() time.Time
	lookPath   func(string) (string, error)
	say        func(context.Context, string)
	mask       func(context.Context, string) error
}

func realDeployTools() deployTools {
	return deployTools{
		startChild: sparkwing.StartChild,
		awaitChild: sparkwing.AwaitChild,
		run:        runDeployCommand,
		registry:   ecrRegistry{},
		oidc:       sparkwing.OIDCToken,
		wait: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
		now:      time.Now,
		lookPath: exec.LookPath,
		say:      func(ctx context.Context, msg string) { sparkwing.Info(ctx, "%s", msg) },
		mask:     sparkwing.Mask,
	}
}

func runDeployCommand(ctx context.Context, c deployCommand) (string, error) {
	var cmd *sparkwing.Cmd
	if c.Stdin != "" {
		cmd = sparkwing.Bash(ctx, shellJoin(append([]string{c.Name}, c.Args...))+` < "$DEPLOY_STDIN"`).Env("DEPLOY_STDIN", c.Stdin)
	} else {
		cmd = sparkwing.Exec(ctx, c.Name, c.Args...)
	}
	cmd = cmd.Dir(c.Dir).EnvMap(c.Env)
	var res sparkwing.ExecResult
	var err error
	if c.Quiet {
		res, err = cmd.Capture()
	} else {
		res, err = cmd.Run()
	}
	if err != nil {
		var execErr *sparkwing.ExecError
		if errors.As(err, &execErr) && !c.Quiet {
			return execErr.Stdout, fmt.Errorf("%s: exit %d", c.Name, execErr.ExitCode)
		}
		return res.Stdout, err
	}
	return res.Stdout, nil
}

func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// safety: mutating deploy actions require act so dry runs can preview them without changing external state.
type deployer struct {
	tools     deployTools
	cfg       deployConfig
	dry       bool
	runID     string
	repoDir   string
	tmp       string
	aws       map[string]string
	resolved  map[string]string
	gate      stagingGateRecord
	awsExpiry time.Time
	cliBuilt  bool
	ecrPass   string
}

func (d *deployer) act(ctx context.Context, desc string, fn func() error) error {
	if d.dry {
		d.tools.say(ctx, "dry-run: would "+desc)
		return nil
	}
	d.tools.say(ctx, desc)
	started := d.tools.now()
	defer func() { d.tools.say(ctx, fmt.Sprintf("elapsed %s: %s", desc, d.tools.now().Sub(started))) }()
	if err := fn(); err != nil {
		return fmt.Errorf("%s: %w", desc, err)
	}
	return nil
}

func (d *deployer) cmd(ctx context.Context, c deployCommand) (string, error) {
	if !filepath.IsAbs(c.Name) {
		path, err := d.tool(c.Name)
		if err != nil {
			return "", err
		}
		c.Name = path
	}
	inUse, err := d.cfg.toolDirInUse(d.dry)
	if err != nil {
		return "", err
	}
	if inUse {
		env := make(map[string]string, len(c.Env)+1)
		for k, v := range c.Env {
			env[k] = v
		}
		env["PATH"] = d.cfg.ToolDir + ":/usr/bin:/bin"
		c.Env = env
	}
	return d.tools.run(ctx, c)
}

// safety: another job on the same runner can plant a binary early on PATH, so
// tools resolve once from the fixed toolchain directory when it exists, and
// every child, kubectl's aws credential plugin included, gets only that PATH.
func (d *deployer) tool(name string) (string, error) {
	if path, ok := d.resolved[name]; ok {
		return path, nil
	}
	inUse, err := d.cfg.toolDirInUse(d.dry)
	if err != nil {
		return "", err
	}
	var path string
	if inUse {
		path = filepath.Join(d.cfg.ToolDir, name)
		if _, err = os.Stat(path); err != nil {
			return "", fmt.Errorf("%s is not in the deploy toolchain %s: %w", name, d.cfg.ToolDir, err)
		}
	} else if path, err = d.tools.lookPath(name); err != nil {
		return "", err
	}
	if d.resolved == nil {
		d.resolved = map[string]string{}
	}
	d.resolved[name] = path
	return path, nil
}

func (d *deployer) secretFile(name, value string) (string, error) {
	path := filepath.Join(d.tmp, name)
	return path, os.WriteFile(path, []byte(value), 0o600)
}

// safety: the machine's AWS profile and config never reach a deploy command,
// so the only credentials are the ones this run's OIDC token earned.
func (d *deployer) assumeRole(ctx context.Context) error {
	return d.act(ctx, "assume "+d.cfg.RoleARN+" with this run's OIDC token", func() error {
		empty, err := d.secretFile("aws-config", "")
		if err != nil {
			return err
		}
		// safety: the AWS CLI reads AWS_PROFILE="" as a profile named "", and an
		// inherited profile would point at the empty config, so it is unset instead.
		if err := os.Unsetenv("AWS_PROFILE"); err != nil {
			return err
		}
		base := map[string]string{
			"AWS_CONFIG_FILE":             empty,
			"AWS_SHARED_CREDENTIALS_FILE": empty,
			"AWS_REGION":                  d.cfg.Region,
			"AWS_DEFAULT_REGION":          d.cfg.Region,
			"AWS_EC2_METADATA_DISABLED":   "true",
		}
		token, err := d.tools.oidc(ctx, "sts.amazonaws.com")
		if err != nil {
			return err
		}
		tokenFile, err := d.secretFile("web-identity-token", token)
		if err != nil {
			return err
		}
		out, err := d.cmd(ctx, deployCommand{Env: base, Quiet: true, Name: "aws", Args: []string{
			"sts", "assume-role-with-web-identity", "--role-arn", d.cfg.RoleARN,
			"--role-session-name", "deploy-" + d.runID, "--web-identity-token", "file://" + tokenFile,
			"--duration-seconds", "3600", "--output", "json",
		}})
		if err != nil {
			return err
		}
		if err := os.Remove(tokenFile); err != nil {
			return err
		}
		var resp struct {
			Credentials struct {
				AccessKeyID     string `json:"AccessKeyId"`
				SecretAccessKey string
				SessionToken    string
				Expiration      time.Time
			}
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil || resp.Credentials.SessionToken == "" {
			return fmt.Errorf("sts returned no credentials: %w", err)
		}
		for _, secret := range []string{resp.Credentials.SecretAccessKey, resp.Credentials.SessionToken} {
			if err := d.tools.mask(ctx, secret); err != nil {
				return err
			}
		}
		base["AWS_ACCESS_KEY_ID"] = resp.Credentials.AccessKeyID
		base["AWS_SECRET_ACCESS_KEY"] = resp.Credentials.SecretAccessKey
		base["AWS_SESSION_TOKEN"] = resp.Credentials.SessionToken
		if kube := d.aws["KUBECONFIG"]; kube != "" {
			base["KUBECONFIG"] = kube
		}
		d.aws, d.awsExpiry = base, resp.Credentials.Expiration
		return nil
	})
}

// safety: the STS session lasts an hour and a deploy, smoke and rollback can
// outlast it, so later steps renew it; when the run can no longer mint a
// token, a session with ten minutes left is still used rather than giving up.
func (d *deployer) refreshRole(ctx context.Context) error {
	err := d.assumeRole(ctx)
	if err != nil && !d.awsExpiry.IsZero() && d.awsExpiry.Sub(d.tools.now()) > 10*time.Minute {
		d.tools.say(ctx, "keeping the current role session until "+d.awsExpiry.UTC().Format(time.RFC3339)+": "+err.Error())
		return nil
	}
	return err
}

func (d *deployer) env(extra ...string) map[string]string {
	env := make(map[string]string, len(d.aws)+len(extra)/2)
	for k, v := range d.aws {
		env[k] = v
	}
	for i := 0; i+1 < len(extra); i += 2 {
		env[extra[i]] = extra[i+1]
	}
	return env
}

func (d *deployer) param(ctx context.Context, name string) (string, error) {
	out, err := d.cmd(ctx, deployCommand{Env: d.env(), Quiet: true, Name: "aws", Args: []string{
		"ssm", "get-parameter", "--name", name, "--with-decryption", "--query", "Parameter.Value", "--output", "text",
	}})
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(out)
	if value == "" {
		return "", fmt.Errorf("SSM parameter %s is empty", name)
	}
	return value, d.tools.mask(ctx, value)
}

func (d *deployer) ecrLogin(ctx context.Context) error {
	return d.act(ctx, "log in to "+d.cfg.Registry, func() error {
		out, err := d.cmd(ctx, deployCommand{Env: d.env(), Quiet: true, Name: "aws", Args: []string{"ecr", "get-login-password"}})
		if err != nil {
			return err
		}
		d.ecrPass = strings.TrimSpace(out)
		return d.tools.mask(ctx, d.ecrPass)
	})
}

func (d *deployer) kubeconfig(ctx context.Context) error {
	path := filepath.Join(d.tmp, "kubeconfig")
	return d.act(ctx, "write a kubeconfig for EKS cluster "+d.cfg.Cluster, func() error {
		if _, err := d.cmd(ctx, deployCommand{Env: d.env(), Quiet: true, Name: "aws", Args: []string{
			"eks", "update-kubeconfig", "--name", d.cfg.Cluster, "--kubeconfig", path, "--alias", "deploy",
		}}); err != nil {
			return err
		}
		d.aws["KUBECONFIG"] = path
		return nil
	})
}

func (d *deployer) kubectl(ctx context.Context, args ...string) (string, error) {
	return d.cmd(ctx, deployCommand{Env: d.env(), Quiet: true, Name: "kubectl", Args: args})
}

// safety: GitHub's published ed25519 host key, so a deploy never trusts a
// host key on first use.
const githubKnownHosts = "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl\n"

// safety: git reads no user or system config, so a deploy commit carries the
// configured bot identity and no host's signing or hook settings.
func (d *deployer) gitEnv(ctx context.Context, keyParam, keyName string) (map[string]string, error) {
	ssh, err := d.tool("ssh")
	if err != nil {
		return nil, err
	}
	key, err := d.param(ctx, keyParam)
	if err != nil {
		return nil, err
	}
	keyFile, err := d.secretFile(keyName, key+"\n")
	if err != nil {
		return nil, err
	}
	hosts, err := d.secretFile("known_hosts", githubKnownHosts)
	if err != nil {
		return nil, err
	}
	global, err := d.secretFile("gitconfig", "")
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"GIT_SSH_COMMAND":     ssh + " -i " + keyFile + " -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=" + hosts,
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_CONFIG_GLOBAL":   global,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     d.cfg.CommitName,
		"GIT_AUTHOR_EMAIL":    d.cfg.CommitEmail,
		"GIT_COMMITTER_NAME":  d.cfg.CommitName,
		"GIT_COMMITTER_EMAIL": d.cfg.CommitEmail,
	}, nil
}

func (d *deployer) git(ctx context.Context, env map[string]string, dir string, args ...string) (string, error) {
	return d.cmd(ctx, deployCommand{Dir: dir, Env: env, Quiet: true, Name: "git", Args: args})
}

func (d *deployer) sourceGit(ctx context.Context, args ...string) (string, error) {
	return d.cmd(ctx, deployCommand{Dir: d.repoDir, Quiet: true, Name: "git", Args: args})
}

func (d *deployer) buildCandidate(ctx context.Context, version string) (deployImages, error) {
	out := deployImages{Version: version, Commit: "<HEAD>", SchemaDigest: "<schema.sql digest>"}
	if out.Version == "" && d.dry {
		out.Version = "<version>"
	}
	out.Controller = imagePin{Repository: d.cfg.Registry + "/sparkwing-controller", Tag: version, Digest: deployDryDigest}
	out.Runner = imagePin{Repository: d.cfg.Registry + "/sparkwing-runner", Tag: version, Digest: deployDryDigest}
	err := d.act(ctx, "resolve the candidate commit, version and store baseline in "+d.repoDir, func() error {
		head, err := d.sourceGit(ctx, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		out.Commit = strings.TrimSpace(head)
		if version == "" {
			if out.Version, err = resolveServiceBuild(ctx, d.sourceGit); err != nil {
				return err
			}
		}
		if !serviceBuildStamp.MatchString(out.Version) && !module.IsPseudoVersion(out.Version) && (!semver.IsValid(out.Version) || semver.Prerelease(out.Version) != "") {
			return fmt.Errorf("deploy version %q must be a release or Go pseudo-version", out.Version)
		}
		out.Controller.Tag, out.Runner.Tag = out.Version, out.Version
		schema, err := os.ReadFile(filepath.Join(d.repoDir, deployStoreSchema))
		if err != nil {
			return err
		}
		out.SchemaDigest = schemaBaselineDigestOf(string(schema))
		return nil
	})
	if err != nil {
		return out, err
	}
	if err := d.requireMainTip(ctx, out.Commit); err != nil {
		return out, err
	}
	if err := d.assumeRole(ctx); err != nil {
		return out, err
	}
	if err := d.ecrLogin(ctx); err != nil {
		return out, err
	}
	var ctl, run string
	if err := d.act(ctx, "look up "+out.Controller.Repository+":"+out.Version+" and "+out.Runner.Repository+":"+out.Version, func() error {
		var err error
		if ctl, err = d.tools.registry.digest(ctx, d.ecrPass, out.Controller.Repository+":"+out.Version); err != nil {
			return err
		}
		if run, err = d.tools.registry.digest(ctx, d.ecrPass, out.Runner.Repository+":"+out.Version); err != nil {
			return err
		}
		if (ctl == "") != (run == "") {
			return fmt.Errorf("only one image of %s is published; registry tags are immutable, so build under a new --version", out.Version)
		}
		return nil
	}); err != nil {
		return out, err
	}
	if ctl != "" && !d.dry {
		for _, ref := range []string{out.Controller.Repository + "@" + ctl, out.Runner.Repository + "@" + run} {
			rev, err := d.tools.registry.revision(ctx, d.ecrPass, ref)
			if err != nil {
				return out, err
			}
			if rev != out.Commit {
				return out, fmt.Errorf("%s was built from %s, not %s; build under a new --version", ref, rev, out.Commit)
			}
		}
		d.tools.say(ctx, "reusing the images already published for "+out.Commit)
		out.Controller.Digest, out.Runner.Digest = ctl, run
		return out, nil
	}
	if err := d.act(ctx, "check that both runtime bases are available before building", func() error {
		for _, ref := range []string{d.cfg.ControllerBase, d.cfg.RunnerBase} {
			digest, err := d.tools.registry.digest(ctx, d.ecrPass, ref)
			if err != nil {
				return fmt.Errorf("read runtime base %s: %w", ref, err)
			}
			if digest == "" {
				return fmt.Errorf("runtime base %s is missing; restore the pinned manifest or select an available runtime base before retrying", ref)
			}
		}
		return nil
	}); err != nil {
		return out, err
	}
	dist := filepath.Join(d.repoDir, "dist")
	if err := d.act(ctx, "build the dashboard with bin/build-web.sh", func() error {
		if err := removeDeployDir(dist); err != nil {
			return err
		}
		_, err := d.cmd(ctx, deployCommand{Dir: d.repoDir, Name: "bash", Args: []string{"bin/build-web.sh", "--reuse"}})
		return err
	}); err != nil {
		return out, err
	}
	for _, arch := range releaseImageArches {
		if err := d.act(ctx, "build linux/"+arch+" binaries "+out.Version+" with bin/build-release-target.sh", func() error {
			_, err := d.cmd(ctx, deployCommand{
				Dir: d.repoDir, Name: "bash", Args: []string{"bin/build-release-target.sh"},
				Env: map[string]string{"GOOS": "linux", "GOARCH": arch, "TAG": out.Version, "GOWORK": "off"},
			})
			return err
		}); err != nil {
			return out, err
		}
	}
	err = d.act(ctx, "publish both images onto the runtime bases with publish-images", func() error {
		if err := writeSHA256Sums(dist); err != nil {
			return err
		}
		if err := d.tools.registry.publish(ctx, d.ecrPass, PublishImagesArgs{
			Dist: dist, Version: out.Version, Revision: out.Commit, Registry: d.cfg.Registry,
			ControllerBase: d.cfg.ControllerBase, RunnerBase: d.cfg.RunnerBase,
		}); err != nil {
			return err
		}
		if out.Controller.Digest, err = d.tools.registry.digest(ctx, d.ecrPass, out.Controller.Repository+":"+out.Version); err != nil {
			return err
		}
		out.Runner.Digest, err = d.tools.registry.digest(ctx, d.ecrPass, out.Runner.Repository+":"+out.Version)
		if err == nil && (out.Controller.Digest == "" || out.Runner.Digest == "") {
			err = errors.New("published images are not readable by tag")
		}
		return err
	})
	return out, err
}

// perf: stale waiters stop before image work or a staging rollout.
func (d *deployer) requireMainTip(ctx context.Context, commit string) error {
	return d.act(ctx, "check that "+commit+" is still the tip of origin/main", func() error {
		out, err := d.sourceGit(ctx, "ls-remote", "--exit-code", "origin", "refs/heads/main")
		if err != nil {
			return err
		}
		fields := strings.Fields(out)
		if len(fields) == 0 || fields[0] != commit {
			return fmt.Errorf("origin/main is at %q, not %s; only main's tip deploys, so a retried or superseded run stops", strings.Join(fields, " "), commit)
		}
		return nil
	})
}

// safety: preserve FETCH_HEAD because Cloud smoke uses it as repository proof.
func (d *deployer) requireMainAncestor(ctx context.Context, commit string) error {
	return d.act(ctx, "check that "+commit+" belongs to origin/main", func() error {
		shallow, err := d.sourceGit(ctx, "rev-parse", "--is-shallow-repository")
		if err != nil {
			return err
		}
		args := []string{"fetch", "--no-tags", "--no-write-fetch-head"}
		if strings.TrimSpace(shallow) == "true" {
			args = append(args, "--depth=1")
		}
		args = append(args, "origin", "+refs/heads/main:refs/remotes/origin/main")
		if _, err := d.sourceGit(ctx, args...); err != nil {
			return err
		}
		tip, err := d.sourceGit(ctx, "rev-parse", "refs/remotes/origin/main")
		if err != nil {
			return err
		}
		if err := d.requireSourceAncestor(ctx, commit, strings.TrimSpace(tip)); err != nil {
			return fmt.Errorf("candidate %s is not an ancestor of origin/main; refusing deployment: %w", commit, err)
		}
		return nil
	})
}

func writeSHA256Sums(dist string) error {
	entries, err := os.ReadDir(dist)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir() || e.Name() == "SHA256SUMS" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dist, e.Name()))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), e.Name())
	}
	return os.WriteFile(filepath.Join(dist, "SHA256SUMS"), []byte(b.String()), 0o644)
}

type deployPhase struct {
	name       string
	deployment func(deployEnv) string
	want       func(deployImages) imagePin
	current    func(valuesPins) imagePin
}

var deployPhases = []deployPhase{
	{
		name:       deployPhaseController,
		deployment: func(e deployEnv) string { return e.ControllerDeployment },
		want:       func(i deployImages) imagePin { return i.Controller },
		current:    func(p valuesPins) imagePin { return p.Controller },
	},
	{
		name:       deployPhaseLauncher,
		deployment: func(e deployEnv) string { return e.LauncherDeployment },
		want:       func(i deployImages) imagePin { return i.Runner },
		current:    func(p valuesPins) imagePin { return p.Launcher },
	},
}

func phaseNamed(name string) deployPhase {
	for _, p := range deployPhases {
		if p.name == name {
			return p
		}
	}
	return deployPhases[0]
}

// safety: the controller ships in its own GitOps commit and must be Ready
// before the launcher's commit, because an older controller refuses a newer
// launcher's requests. Each environment's credentials come from the role.
func (d *deployer) deployEnvironment(ctx context.Context, env deployEnv, images deployImages, gitEnv map[string]string) (envDeployment, error) {
	rec := envDeployment{Environment: env.Name, Images: images}
	dir := filepath.Join(d.tmp, "gitops-"+env.Name)
	values := filepath.Join(dir, env.GitopsDir, "helm-values.yaml")
	if err := d.act(ctx, "clone "+d.cfg.GitopsURL+" ("+d.cfg.GitopsBranch+") into "+dir, func() error {
		_, err := d.git(ctx, gitEnv, "", "clone", "--quiet", "--branch", d.cfg.GitopsBranch, "--single-branch", d.cfg.GitopsURL, dir)
		return err
	}); err != nil {
		return rec, err
	}
	if err := d.act(ctx, "read the image pins in "+env.GitopsDir+"/helm-values.yaml", func() error {
		body, err := os.ReadFile(values)
		if err != nil {
			return err
		}
		rec.Previous, err = readValuesPins(string(body))
		return err
	}); err != nil {
		return rec, err
	}
	if err := d.act(ctx, "retain candidate and rollback image manifests outside ECR rolling pruning", func() error {
		for _, pin := range []imagePin{images.Controller, images.Runner, rec.Previous.Controller, rec.Previous.Launcher, rec.Previous.Job} {
			if err := d.tools.registry.retain(ctx, d.ecrPass, pin); err != nil {
				return fmt.Errorf("retain %s: %w", pin.ref(), err)
			}
		}
		return nil
	}); err != nil {
		return rec, err
	}
	rec.WindowStart = d.tools.now().UTC().Format(time.RFC3339)
	if !d.dry && rec.Previous.Controller.Digest == images.Controller.Digest && rec.Previous.Launcher.Digest == images.Runner.Digest {
		d.tools.say(ctx, env.Name+" already runs controller "+images.Controller.Digest+" and runner "+images.Runner.Digest)
		return rec, nil
	}
	if env.Name == d.cfg.Production.Name {
		if err := d.act(ctx, "check that "+images.Commit+" and "+images.Version+" advance the production source without lowering its version", func() error {
			return d.requireNotOlder(ctx, env, rec.Previous, images)
		}); err != nil {
			return rec, err
		}
	}
	if env.Name == d.cfg.Production.Name {
		if err := d.act(ctx, "compare production and candidate controller schema versions before changing images", func() error {
			revision, err := d.tools.registry.revision(ctx, d.ecrPass, rec.Previous.Controller.Repository+"@"+rec.Previous.Controller.Digest)
			if err != nil {
				return err
			}
			if !fullCommitRE.MatchString(revision) {
				return errors.New("previous controller has no valid source revision; production was not changed")
			}
			previous, err := d.sourceGit(ctx, "show", revision+":internal/store/store.go")
			if err != nil {
				// safety: the smoke uses FETCH_HEAD as origin proof, so schema lookup must preserve it.
				if _, err := d.sourceGit(ctx, "fetch", "--no-tags", "--no-write-fetch-head", "origin", revision); err != nil {
					return err
				}
				previous, err = d.sourceGit(ctx, "show", revision+":internal/store/store.go")
				if err != nil {
					return err
				}
			}
			if rec.PreviousSchema, err = readDeploySchemaVersion(previous); err != nil {
				return err
			}
			candidate, err := d.sourceGit(ctx, "show", images.Commit+":internal/store/store.go")
			if err != nil {
				return err
			}
			rec.CandidateSchema, err = readDeploySchemaVersion(candidate)
			return err
		}); err != nil {
			return rec, err
		}
	}
	for _, phase := range deployPhases {
		want := phase.want(images)
		if !d.dry && phase.current(rec.Previous).Digest == want.Digest {
			d.tools.say(ctx, env.Name+" "+phase.name+" already runs "+want.Digest)
			continue
		}
		commit, err := d.pinAndPush(ctx, env, dir, phase, images, gitEnv)
		if err != nil {
			return rec, err
		}
		rec.Changes = append(rec.Changes, gitopsChange{Phase: phase.name, Commit: commit})
		if err := d.waitRollout(ctx, env, commit, phase, want.Digest, gitEnv); err != nil {
			return rec, err
		}
	}
	return rec, nil
}

func (d *deployer) requireNotOlder(ctx context.Context, env deployEnv, previous valuesPins, images deployImages) error {
	for _, phase := range deployPhases {
		pin := phase.current(previous)
		previous, err := deployVersionOrder(pin.Tag)
		if err != nil {
			return d.firstDeployError(env, pin, err.Error())
		}
		candidate, err := deployVersionOrder(images.Version)
		if err != nil {
			return err
		}
		if semver.Compare(candidate, previous) < 0 {
			return fmt.Errorf("%s runs %s, newer than the candidate %s; this pipeline never moves an environment back, so roll back by reverting the GitOps commit", env.Name, pin.Tag, images.Version)
		}
		revision, err := d.tools.registry.revision(ctx, d.ecrPass, pin.Repository+"@"+pin.Digest)
		if err != nil {
			return err
		}
		if !fullCommitRE.MatchString(revision) {
			return errors.New("production revision is missing or invalid; production was not changed")
		}
		if revision == images.Commit && pin.Digest != phase.want(images).Digest {
			return fmt.Errorf("production revision %s already matches the candidate with different image digests; use a newer source commit; production was not changed", revision)
		}
		if err := d.requireSourceAncestor(ctx, revision, images.Commit); err != nil {
			return fmt.Errorf("production revision %s is not an ancestor of candidate %s; production was not changed: %w", revision, images.Commit, err)
		}
	}
	return nil
}

func (d *deployer) requireSourceAncestor(ctx context.Context, ancestor, commit string) error {
	_, err := d.sourceGit(ctx, "merge-base", "--is-ancestor", ancestor, commit)
	if err == nil {
		return nil
	}
	// perf: recent deployment ancestry needs only a short span of commits, not every release's history.
	for _, depth := range []string{"--deepen=64", "--unshallow"} {
		shallow, depthErr := d.sourceGit(ctx, "rev-parse", "--is-shallow-repository")
		if depthErr != nil {
			return depthErr
		}
		if strings.TrimSpace(shallow) != "true" {
			return err
		}
		if _, err := d.sourceGit(ctx, "fetch", "--no-tags", "--no-write-fetch-head", depth, "origin", commit); err != nil {
			return err
		}
		if _, err = d.sourceGit(ctx, "merge-base", "--is-ancestor", ancestor, commit); err == nil {
			return nil
		}
	}
	return err
}

// safety: an image without an ordered version needs a manual first deploy
// because the production downgrade guard cannot compare it with the candidate.
func (d *deployer) firstDeployError(env deployEnv, deployed imagePin, why string) error {
	return fmt.Errorf("%s runs %s, and %s. Deploy once by hand: confirm the live store with "+
		"`SELECT version, baseline_digest FROM sparkwing_schema_version` matches this candidate's baseline, pin controller.image.tag and "+
		"sparkwing-runner-bundle.launcher.image.tag, jobImage.tag and jobImage.digest in %s/helm-values.yaml to images built by this "+
		"pipeline (they carry the revision label), render without the cluster-scoped documents kikd-infra applies with "+
		"`helm template %s charts/sparkwing-full --namespace %s -f %s/helm-values.yaml | yq 'select(.metadata.namespace != null)' > %s/rendered.yaml`, "+
		"and push; later deploys then run here", env.Name, deployed.ref(), why, env.GitopsDir, env.HelmRelease, env.Namespace, env.GitopsDir, env.GitopsDir)
}

func (d *deployer) pinAndPush(ctx context.Context, env deployEnv, dir string, phase deployPhase, images deployImages, gitEnv map[string]string) (string, error) {
	want := phase.want(images)
	values := filepath.Join(env.GitopsDir, "helm-values.yaml")
	rendered := filepath.Join(env.GitopsDir, "rendered.yaml")
	if err := d.act(ctx, "pin the "+env.Name+" "+phase.name+" to "+want.ref()+" in "+values, func() error {
		// safety: the clone is repository content, so a symlinked values file must not
		// redirect this rewrite outside the clone.
		root, err := os.OpenRoot(dir)
		if err != nil {
			return err
		}
		defer root.Close() //nolint:errcheck // read-only handle; the write below reports its own error.
		body, err := root.ReadFile(values)
		if err != nil {
			return err
		}
		pinned, err := pinValues(string(body), phase.name, want)
		if err != nil {
			return err
		}
		return root.WriteFile(values, []byte(pinned), 0o644)
	}); err != nil {
		return "", err
	}
	chart := filepath.Join(d.repoDir, deployChart)
	if err := d.act(ctx, "render "+rendered+" with helm template "+env.HelmRelease+" "+chart+" --namespace "+env.Namespace, func() error {
		out, err := d.cmd(ctx, deployCommand{Quiet: true, Name: "helm", Args: []string{
			"template", env.HelmRelease, chart, "--namespace", env.Namespace, "-f", filepath.Join(dir, values),
		}})
		if err != nil {
			return err
		}
		if err := d.verifyClusterAdmission(ctx, out); err != nil {
			return err
		}
		if phase.name == deployPhaseController {
			previous, err := os.ReadFile(filepath.Join(dir, rendered))
			if err != nil {
				return err
			}
			if out, err = spliceControllerDeployment(string(previous), out, env.Namespace, env.ControllerDeployment); err != nil {
				return err
			}
		}
		out, dropped, err := dropClusterScoped(out)
		if err != nil {
			return err
		}
		if len(dropped) > 0 {
			d.tools.say(ctx, "left out of "+rendered+", applied through kikd-infra k8s/sparkwing-cluster/: "+strings.Join(dropped, ", "))
		}
		if !strings.Contains(out, "@"+want.Digest) {
			return fmt.Errorf("the render does not reference %s", want.Digest)
		}
		return os.WriteFile(filepath.Join(dir, rendered), []byte(out), 0o644)
	}); err != nil {
		return "", err
	}
	commit := "<" + env.Name + " " + phase.name + " commit>"
	msg := fmt.Sprintf("deploy(%s): %s %s from sparkwing %s", env.Name, phase.name, images.Version, shortCommit(images.Commit))
	if err := d.act(ctx, "commit \""+msg+"\" and push it to "+d.cfg.GitopsBranch, func() error {
		if _, err := d.git(ctx, gitEnv, dir, "add", "--", values, rendered); err != nil {
			return err
		}
		if _, err := d.git(ctx, gitEnv, dir, "commit", "--quiet", "--no-verify", "-m", msg, "--", values, rendered); err != nil {
			return err
		}
		head, err := d.git(ctx, gitEnv, dir, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		commit = strings.TrimSpace(head)
		return d.pushGitops(ctx, env, dir, gitEnv, images.Commit)
	}); err != nil {
		return "", err
	}
	return commit, nil
}

// safety: merge the other environment's pushes without rewriting the phase
// commits that production rollback must still be able to revert.
func (d *deployer) pushGitops(ctx context.Context, env deployEnv, dir string, gitEnv map[string]string, source string) error {
	for attempt := 0; ; attempt++ {
		if source != "" && env.Name == d.cfg.Production.Name {
			if err := d.requireMainAncestor(ctx, source); err != nil {
				return fmt.Errorf("refusing production GitOps push for %s: %w", source, err)
			}
		}
		_, pushErr := d.git(ctx, gitEnv, dir, "push", "--quiet", "origin", "HEAD:refs/heads/"+d.cfg.GitopsBranch)
		if pushErr == nil || attempt == 3 {
			return pushErr
		}
		if _, err := d.git(ctx, gitEnv, dir, "fetch", "--quiet", "origin", d.cfg.GitopsBranch); err != nil {
			return errors.Join(pushErr, err)
		}
		if _, err := d.git(ctx, gitEnv, dir, "merge-base", "--is-ancestor", "FETCH_HEAD", "HEAD"); err == nil {
			return pushErr
		}
		if _, err := d.git(ctx, gitEnv, dir, "merge", "--quiet", "--no-edit", "FETCH_HEAD"); err != nil {
			return errors.Join(pushErr, err)
		}
	}
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

func (d *deployer) waitRollout(ctx context.Context, env deployEnv, commit string, phase deployPhase, digest string, gitEnv map[string]string) error {
	if err := d.act(ctx, "refresh Argo application "+env.ArgoApp+" after GitOps push "+commit, func() error {
		_, err := d.kubectl(ctx, "-n", "argocd", "annotate", "application", env.ArgoApp, "argocd.argoproj.io/refresh=hard", "--overwrite")
		if err != nil {
			return fmt.Errorf("refreshing Argo needs patch permission on application %s: %w", env.ArgoApp, err)
		}
		return nil
	}); err != nil {
		return err
	}

	if err := d.act(ctx, "wait for Argo application "+env.ArgoApp+" to be Synced and Healthy at "+commit, func() error {
		deadline := d.tools.now().Add(d.cfg.ArgoTimeout)
		last := ""
		for {
			out, err := d.kubectl(ctx, "-n", "argocd", "get", "application", env.ArgoApp, "-o", "json")
			if err != nil {
				return err
			}
			var app struct {
				Status struct {
					Sync   struct{ Status, Revision string }
					Health struct{ Status string }
				}
			}
			if err := json.Unmarshal([]byte(out), &app); err != nil {
				return err
			}
			last = fmt.Sprintf("%s %s %s", app.Status.Sync.Revision, app.Status.Sync.Status, app.Status.Health.Status)
			if app.Status.Sync.Status == "Synced" && app.Status.Health.Status == "Healthy" {
				if app.Status.Sync.Revision == commit {
					return nil
				}
				if fullCommitRE.MatchString(app.Status.Sync.Revision) && fullCommitRE.MatchString(commit) {
					matches, err := d.gitopsEnvironmentUnchanged(ctx, env, commit, app.Status.Sync.Revision, gitEnv)
					if err != nil {
						return err
					}
					if matches {
						return nil
					}
				}
			}
			if !d.tools.now().Before(deadline) {
				return fmt.Errorf("still %s after %s", last, d.cfg.ArgoTimeout)
			}
			if err := d.tools.wait(ctx, time.Second); err != nil {
				return err
			}
		}
	}); err != nil {
		return err
	}
	deployment := phase.deployment(env)
	return d.act(ctx, "wait for deployment "+env.Namespace+"/"+deployment+" to roll out "+digest, func() error {
		if _, err := d.kubectl(ctx, "-n", env.Namespace, "rollout", "status", "deployment/"+deployment, "--timeout="+d.cfg.RolloutTimeout.String()); err != nil {
			return err
		}
		image, err := d.kubectl(ctx, "-n", env.Namespace, "get", "deployment", deployment, "-o",
			`jsonpath={.spec.template.spec.containers[?(@.name=="`+phase.name+`")].image}`)
		if err != nil {
			return err
		}
		if !strings.HasSuffix(strings.TrimSpace(image), "@"+digest) {
			return fmt.Errorf("%s runs %q, not %s", deployment, strings.TrimSpace(image), digest)
		}
		return nil
	})
}

func (d *deployer) gitopsEnvironmentUnchanged(ctx context.Context, env deployEnv, commit, observed string, gitEnv map[string]string) (bool, error) {
	dir := filepath.Join(d.tmp, "gitops-"+env.Name)
	if _, err := d.git(ctx, gitEnv, dir, "fetch", "--quiet", "--no-tags", "origin", observed); err != nil {
		return false, err
	}
	base, err := d.git(ctx, gitEnv, dir, "merge-base", commit, observed)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(base) != commit {
		return false, nil
	}
	changed, err := d.git(ctx, gitEnv, dir, "diff", "--name-only", commit, observed, "--", env.GitopsDir)
	return strings.TrimSpace(changed) == "" && err == nil, err
}

// safety: a cancelled or timed-out job still owes production its rollback, so
// it runs detached from the job's cancellation under its own deadline, with a
// renewed role session because the deploy and smoke may have used up the hour.
func (d *deployer) rollbackDetached(ctx context.Context, env deployEnv, rec envDeployment, gitEnv map[string]string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.cfg.rollbackBudget())
	defer cancel()
	if err := d.refreshRole(ctx); err != nil {
		return err
	}
	return d.rollback(ctx, env, rec, gitEnv)
}

// safety: rollback reverts this run's GitOps commits newest first, so the
// launcher returns to its old image before the controller does and every
// other value goes back to exactly what was rendered before.
func (d *deployer) rollback(ctx context.Context, env deployEnv, rec envDeployment, gitEnv map[string]string) error {
	dir := filepath.Join(d.tmp, "gitops-"+env.Name)
	var errs []error
	for i := len(rec.Changes) - 1; i >= 0; i-- {
		change := rec.Changes[i]
		phase := phaseNamed(change.Phase)
		revert := "<revert of " + change.Commit + ">"
		err := d.act(ctx, "revert "+env.Name+" "+change.Phase+" commit "+change.Commit+" and push it", func() error {
			if _, err := d.git(ctx, gitEnv, dir, "fetch", "--quiet", "origin", d.cfg.GitopsBranch); err != nil {
				return err
			}
			if _, err := d.git(ctx, gitEnv, dir, "checkout", "--quiet", "-B", d.cfg.GitopsBranch, "FETCH_HEAD"); err != nil {
				return err
			}
			commit, err := gitRevision(change.Commit)
			if err != nil {
				return err
			}
			if _, err := d.git(ctx, gitEnv, dir, "revert", "--no-edit", "--end-of-options", commit); err != nil {
				return err
			}
			head, err := d.git(ctx, gitEnv, dir, "rev-parse", "HEAD")
			if err != nil {
				return err
			}
			revert = strings.TrimSpace(head)
			return d.pushGitops(ctx, env, dir, gitEnv, "")
		})
		if err == nil {
			err = d.waitRollout(ctx, env, revert, phase, phase.current(rec.Previous).Digest, gitEnv)
		}
		if err != nil {
			errs = append(errs, err)
			break
		}
	}
	return errors.Join(errs...)
}

// safety: the CLI is built from the candidate checkout and runs under a
// throwaway HOME and SPARKWING_HOME, so no machine's profiles or store are read.
func (d *deployer) buildCLI(ctx context.Context) error {
	if d.cliBuilt {
		return nil
	}
	home := filepath.Join(d.tmp, "cli-home")
	return d.act(ctx, "build the candidate CLI into "+d.cliBin(), func() error {
		if err := os.MkdirAll(home, 0o700); err != nil {
			return err
		}
		_, err := d.cmd(ctx, deployCommand{Dir: d.repoDir, Env: map[string]string{"GOWORK": "off", "GOFLAGS": "-modcacherw"}, Name: "go", Args: []string{"build", "-o", d.cliBin(), "./cmd/sparkwing"}})
		return err
	})
}

func (d *deployer) cliBin() string { return filepath.Join(d.tmp, "sparkwing") }

func (d *deployer) sw(ctx context.Context, stdin string, args ...string) (string, error) {
	home := filepath.Join(d.tmp, "cli-home")
	if err := cachecontrol.ShareWithHome(filepath.Join(home, ".sparkwing")); err != nil {
		d.tools.say(ctx, "pipeline cache sharing unavailable; use the private job cache: "+err.Error())
	}
	return d.cmd(ctx, deployCommand{Stdin: stdin, Quiet: true, Name: d.cliBin(), Args: args, Env: map[string]string{
		"HOME": home, "SPARKWING_HOME": filepath.Join(home, ".sparkwing"), "XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"GOFLAGS": "-modcacherw",
	}})
}

func (d *deployer) addProfile(ctx context.Context, profile, url, tokenParam string) error {
	return d.act(ctx, "add CLI profile "+profile+" for "+url+" with the token in "+tokenParam, func() error {
		token, err := d.param(ctx, tokenParam)
		if err != nil {
			return err
		}
		return d.profileToken(ctx, profile, url, token)
	})
}

func (d *deployer) profileToken(ctx context.Context, profile, url, token string) error {
	file, err := d.secretFile("token-"+profile, token)
	if err != nil {
		return err
	}
	_, err = d.sw(ctx, file, "profiles", "add", "--profile", profile, "--controller", url, "--token-stdin")
	return errors.Join(err, os.Remove(file))
}

func (d *deployer) triggerRun(ctx context.Context, profile, pipeline, checkout, ref string, runID *string, pipelineArgs ...string) error {
	args := []string{"pipeline", "trigger", pipeline, "-C", checkout, "--profile", profile, "--runner", "cloud", "--detach", "-o", "json"}
	if ref != "" {
		args = append(args, "--ref", ref)
	}
	if len(pipelineArgs) > 0 {
		args = append(append(args, "--"), pipelineArgs...)
	}
	return d.act(ctx, "trigger "+pipeline+" from "+checkout+" on "+profile+" Cloud", func() error {
		out, err := d.sw(ctx, "", args...)
		if err != nil {
			return err
		}
		if *runID = acceptedRunID(out); *runID == "" {
			return fmt.Errorf("trigger printed no run_id: %q", out)
		}
		d.tools.say(ctx, pipeline+" on "+profile+" is "+*runID)
		return nil
	})
}

func acceptedRunID(out string) string {
	dec := json.NewDecoder(strings.NewReader(out))
	for {
		var doc struct {
			RunID string `json:"run_id"`
		}
		if err := dec.Decode(&doc); err != nil {
			return ""
		}
		if doc.RunID != "" {
			return doc.RunID
		}
	}
}

func (d *deployer) runTimeout(profile string) time.Duration {
	if slices.Contains(d.cfg.SmokeProfiles, profile) {
		return d.cfg.SmokeTimeout
	}
	return d.cfg.RunTimeout
}

func (d *deployer) followRun(ctx context.Context, profile string, runID *string) error {
	return d.act(ctx, "follow the run on "+profile+" until it succeeds", func() error {
		_, err := d.sw(ctx, "", "runs", "status", "--run", *runID, "--profile", profile, "--follow", "--poll", "1s", "--timeout", d.runTimeout(profile).String(), "-o", "json")
		if err != nil {
			return fmt.Errorf("run %s did not succeed: %w", *runID, err)
		}
		return nil
	})
}

func (d *deployer) stagingGate(ctx context.Context, images deployImages) (stagingGateRecord, error) {
	rec := stagingGateRecord{Images: images, RunID: d.runID, GatedAt: d.tools.now().UTC().Format(time.RFC3339)}
	if err := d.assumeRole(ctx); err != nil {
		return rec, err
	}
	if err := d.buildCLI(ctx); err != nil {
		return rec, err
	}
	const profile = "staging"
	if err := d.addProfile(ctx, profile, d.cfg.Staging.ControllerURL, d.cfg.Params.StagingToken); err != nil {
		return rec, err
	}
	if err := d.triggerRun(ctx, profile, "deploy-smoke", d.repoDir, images.Commit, &rec.SmokeRun, "--child"); err != nil {
		return rec, err
	}
	reference := filepath.Join(d.tmp, "reference")
	if err := d.act(ctx, "clone the reference repository "+d.cfg.ReferenceURL, func() error {
		env, err := d.gitEnv(ctx, d.cfg.Params.ReferenceKey, "reference-key")
		if err != nil {
			return err
		}
		_, err = d.git(ctx, env, "", "clone", "--quiet", "--depth", "1", d.cfg.ReferenceURL, reference)
		return err
	}); err != nil {
		return rec, err
	}
	if err := d.triggerRun(ctx, profile, d.cfg.ReferenceName, reference, "", &rec.ReferenceRun); err != nil {
		return rec, err
	}
	if err := errors.Join(d.followRun(ctx, profile, &rec.SmokeRun), d.followRun(ctx, profile, &rec.ReferenceRun)); err != nil {
		return rec, err
	}
	var err error
	rec.ScanRun, err = d.submitScan(ctx, d.cfg.Staging, images)
	if err == nil {
		rec.CLIDigest = d.cacheCLI(ctx)
		rec.CLIPlatform = runtime.GOOS + "/" + runtime.GOARCH
	}
	return rec, err
}

// safety: nodes outlive a run only briefly, so NodeClaims are sampled while
// the smoke runs rather than counted afterwards.
func (d *deployer) productionSmoke(ctx context.Context, images deployImages, since string) error {
	env := d.cfg.Production
	runs := make([]string, len(d.cfg.SmokeProfiles))
	for i, team := range d.cfg.SmokeProfiles {
		if err := d.addProfile(ctx, team, env.ControllerURL, d.cfg.Params.SmokeToken); err != nil {
			return err
		}
		if err := d.triggerRun(ctx, team, "deploy-smoke", d.repoDir, images.Commit, &runs[i]); err != nil {
			return err
		}
	}
	claims := make([]map[string]bool, len(runs))
	err := d.act(ctx, "follow production smoke runs and record the NodeClaims each one uses", func() error {
		done := make(chan struct{})
		waitCtx, stopWaiting := context.WithCancel(ctx)
		defer stopWaiting()
		var wg sync.WaitGroup
		errs := make([]error, len(runs))
		for i, team := range d.cfg.SmokeProfiles {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = d.followRun(ctx, team, &runs[i])
			}()
		}
		go func() {
			wg.Wait()
			close(done)
			stopWaiting()
		}()
		for i := range claims {
			claims[i] = map[string]bool{}
		}
		for {
			finished := false
			select {
			case <-done:
				finished = true
			default:
			}
			observeErr := d.observeClaims(ctx, runs, claims)
			if observeErr != nil && d.refreshRole(ctx) == nil {
				observeErr = d.observeClaims(ctx, runs, claims)
			}
			if finished {
				return errors.Join(append(errs, observeErr)...)
			}
			if observeErr != nil {
				d.tools.say(ctx, "NodeClaim observation: "+observeErr.Error())
			}
			if err := d.tools.wait(waitCtx, d.cfg.PollInterval); err != nil {
				if ctx.Err() == nil && waitCtx.Err() != nil {
					continue
				}
				<-done
				return err
			}
		}
	})
	if err != nil {
		return err
	}
	if err := d.refreshRole(ctx); err != nil {
		return err
	}
	return d.act(ctx, "check each smoke run used one NodeClaim and the "+env.Name+" logs since "+since+" hold no WARN or ERROR", func() error {
		for i, run := range runs {
			if len(claims[i]) != 1 {
				names := make([]string, 0, len(claims[i]))
				for n := range claims[i] {
					names = append(names, n)
				}
				sort.Strings(names)
				return fmt.Errorf("smoke run %s used %d NodeClaims %v, want 1", run, len(names), names)
			}
		}
		logs, err := d.kubectl(ctx, "-n", env.Namespace, "logs", "-l", "app.kubernetes.io/component in (controller,launcher)",
			"--since-time="+since, "--all-containers", "--prefix", "--tail=-1", "--max-log-requests=20")
		if err != nil {
			return err
		}
		if dirty := dirtyLogLines(logs); len(dirty) > 0 {
			if len(dirty) > 5 {
				dirty = dirty[:5]
			}
			return fmt.Errorf("%s logged WARN or ERROR since %s:\n%s", env.Name, since, strings.Join(dirty, "\n"))
		}
		return nil
	})
}

func (d *deployer) observeClaims(ctx context.Context, runs []string, claims []map[string]bool) error {
	nodesOut, err := d.kubectl(ctx, "get", "nodes", "-o", "json")
	if err != nil {
		return err
	}
	var nodes struct {
		Items []struct {
			Metadata struct {
				Name        string
				Annotations map[string]string
			}
		}
	}
	if err := json.Unmarshal([]byte(nodesOut), &nodes); err != nil {
		return err
	}
	nodeRun := map[string]int{}
	for _, n := range nodes.Items {
		var used struct {
			RunID string `json:"run_id"`
		}
		if json.Unmarshal([]byte(n.Metadata.Annotations["sparkwing.dev/used-run"]), &used) != nil {
			continue
		}
		for i, run := range runs {
			if used.RunID == run {
				nodeRun[n.Metadata.Name] = i
			}
		}
	}
	if len(nodeRun) == 0 {
		return nil
	}
	claimsOut, err := d.kubectl(ctx, "get", "nodeclaims", "-o", "json")
	if err != nil {
		return err
	}
	var list struct {
		Items []struct {
			Metadata struct{ Name string }
			Status   struct{ NodeName string }
		}
	}
	if err := json.Unmarshal([]byte(claimsOut), &list); err != nil {
		return err
	}
	for _, c := range list.Items {
		if i, ok := nodeRun[c.Status.NodeName]; ok {
			claims[i][c.Metadata.Name] = true
		}
	}
	return nil
}

type ecrRegistry struct{}

func (ecrRegistry) options(ctx context.Context, password string) []remote.Option {
	return []remote.Option{remote.WithContext(ctx), remote.WithAuth(&authn.Basic{Username: "AWS", Password: password})}
}

func (r ecrRegistry) digest(ctx context.Context, password, ref string) (string, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", err
	}
	desc, err := remote.Head(parsed, r.options(ctx, password)...)
	if err != nil {
		var terr *transport.Error
		if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
			return "", nil
		}
		return "", err
	}
	return desc.Digest.String(), nil
}

// safety: an index carries no config, so the label is read from its first
// platform image; publish-images labels every platform the same way.
func (r ecrRegistry) revision(ctx context.Context, password, ref string) (string, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", err
	}
	idx, err := remote.Index(parsed, r.options(ctx, password)...)
	if err != nil {
		return "", err
	}
	manifest, err := idx.IndexManifest()
	if err != nil {
		return "", err
	}
	if len(manifest.Manifests) == 0 {
		return "", fmt.Errorf("%s lists no images", ref)
	}
	img, err := idx.Image(manifest.Manifests[0].Digest)
	if err != nil {
		return "", err
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return "", err
	}
	return cfg.Config.Labels["org.opencontainers.image.revision"], nil
}

func (r ecrRegistry) publish(ctx context.Context, password string, in PublishImagesArgs) error {
	refs, err := publishReleaseImages(ctx, in, remote.WithAuth(&authn.Basic{Username: "AWS", Password: password}))
	for _, ref := range refs {
		sparkwing.Info(ctx, "published %s (linux/amd64, linux/arm64)", ref)
	}
	return err
}

func (d *deployer) recordUnsafeRollback(ctx context.Context, rec envDeployment, cause error) error {
	feedback, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	detail := fmt.Sprintf("deploy %s, candidate %s: schema %d -> %d; automatic rollback suppressed; candidate images retained; roll forward to repair: %v", d.runID, rec.Images.Commit, rec.PreviousSchema, rec.CandidateSchema, cause)
	d.tools.say(feedback, detail)
	body, err := json.Marshal([]map[string]string{{"fingerprint": "production-schema-upgrade", "severity": "critical", "title": "Production deploy failed after a schema upgrade; roll forward required", "detail": detail}})
	if err != nil {
		return err
	}
	file := filepath.Join(d.tmp, "rollback-findings.json")
	if err := os.WriteFile(file, body, 0o600); err != nil {
		return err
	}
	_, err = d.cmd(feedback, deployCommand{Name: "bitwing", Args: []string{"finding", "record", "--source", "sparkwing-production-rollback", "--file", file}})
	return err
}
