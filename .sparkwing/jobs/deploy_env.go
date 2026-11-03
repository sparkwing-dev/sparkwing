package jobs

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// safety: every external system the deploy touches is named here, so a test
// points each one at a fake.
type deployConfig struct {
	Region         string
	Cluster        string
	Registry       string
	ControllerBase string
	RunnerBase     string
	RoleARN        string
	GitopsURL      string
	GitopsBranch   string
	ReferenceURL   string
	ReferenceName  string
	CommitName     string
	CommitEmail    string
	Params         deployParams
	Staging        deployEnv
	Production     deployEnv
	SmokeTeams     []string
	ArgoTimeout    time.Duration
	RunTimeout     time.Duration
	PollInterval   time.Duration
}

type deployParams struct {
	GitopsKey      string
	ReferenceKey   string
	StagingToken   string
	SmokeTokenBase string
}

type deployEnv struct {
	Name                 string
	GitopsDir            string
	HelmRelease          string
	Namespace            string
	ArgoApp              string
	ControllerDeployment string
	LauncherDeployment   string
	ControllerURL        string
}

func defaultDeployConfig() deployConfig {
	const ecr = "633280902600.dkr.ecr.us-west-2.amazonaws.com"
	return deployConfig{
		Region:   "us-west-2",
		Cluster:  "kikd-prod",
		Registry: ecr,
		// safety: the qa.20 runtime bases every QA and v0.68 image was built on;
		// rebuild them with build-images --runtime-base and update these together.
		ControllerBase: ecr + "/sparkwing-controller@sha256:db2c67bb6189f8ea33e92df7f8ff4541e9da5b8fe00607bad265a8e5743dc4a3",
		RunnerBase:     ecr + "/sparkwing-runner@sha256:055651afe046f9eb44a8d1eba12d560c189057dd04339cad298f6e9c926f1c8d",
		RoleARN:        "arn:aws:iam::633280902600:role/sparkwing-deploy",
		GitopsURL:      "git@github.com:koreyGambill/kikd-gitops.git",
		GitopsBranch:   "main",
		ReferenceURL:   "git@github.com:sparkwing-dev/sparkwing-ci-smoke.git",
		ReferenceName:  "hello",
		CommitName:     "Sparkwing deploy",
		CommitEmail:    "deploy@sparkwing.dev",
		Params: deployParams{
			GitopsKey:      "/kikd-prod/sparkwing/deploy/gitops-deploy-key",
			ReferenceKey:   "/kikd-prod/sparkwing/deploy/ci-smoke-deploy-key",
			StagingToken:   "/kikd-prod/sparkwing/deploy/staging-gate-token",
			SmokeTokenBase: "/kikd-prod/sparkwing/deploy/smoke-token-",
		},
		Staging: deployEnv{
			Name:                 "staging",
			GitopsDir:            "sparkwing-staging",
			HelmRelease:          "sparkwing-staging",
			Namespace:            "sparkwing-staging",
			ArgoApp:              "sparkwing-staging",
			ControllerDeployment: "sparkwing-staging-sparkwing-full-controller",
			LauncherDeployment:   "sparkwing-staging-sparkwing-runner-bundle-launcher",
			ControllerURL:        "https://staging.sparkwing.dev",
		},
		Production: deployEnv{
			Name:                 "production",
			GitopsDir:            "sparkwing",
			HelmRelease:          "sparkwing",
			Namespace:            "sparkwing",
			ArgoApp:              "sparkwing",
			ControllerDeployment: "sparkwing-sparkwing-full-controller",
			LauncherDeployment:   "sparkwing-sparkwing-runner-bundle-launcher",
			ControllerURL:        "https://api.sparkwing.dev",
		},
		SmokeTeams:   []string{"qa-team", "qa-team-b"},
		ArgoTimeout:  10 * time.Minute,
		RunTimeout:   45 * time.Minute,
		PollInterval: 5 * time.Second,
	}
}

type imagePin struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Digest     string `json:"digest"`
}

func (p imagePin) ref() string { return p.Repository + ":" + p.Tag + "@" + p.Digest }

type deployImages struct {
	Commit       string   `json:"commit"`
	Version      string   `json:"version"`
	SchemaDigest string   `json:"schema_digest"`
	Controller   imagePin `json:"controller"`
	Runner       imagePin `json:"runner"`
}

func (a deployImages) sameDigests(b deployImages) bool {
	return a.Controller.Digest != "" && a.Runner.Digest != "" &&
		a.Controller.Digest == b.Controller.Digest && a.Runner.Digest == b.Runner.Digest
}

type valuesPins struct {
	Controller imagePin `json:"controller"`
	Launcher   imagePin `json:"launcher"`
	Job        imagePin `json:"job"`
}

type gitopsChange struct {
	Phase  string `json:"phase"`
	Commit string `json:"commit"`
}

// safety: rollback and the post-deploy cron find what an environment runs
// through RefToLastRun("deploy", "staging-deploy" or "production").
type envDeployment struct {
	Environment string         `json:"environment"`
	Images      deployImages   `json:"images"`
	Previous    valuesPins     `json:"previous"`
	Changes     []gitopsChange `json:"changes"`
	WindowStart string         `json:"window_start"`
}

type stagingGateRecord struct {
	Images       deployImages `json:"images"`
	RunID        string       `json:"run_id"`
	ExampleRun   string       `json:"example_run"`
	ReferenceRun string       `json:"reference_run"`
}

const (
	deployStoreSchema = "internal/store/schema.sql"
	deployChart       = "charts/sparkwing-full"
)

// safety: the store records sha256 of the embedded schema.sql with CRLF folded
// to LF, and refuses to open under a binary whose digest differs.
func schemaBaselineDigestOf(schema string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.ReplaceAll(schema, "\r\n", "\n"))))
}

var yamlKeyRE = regexp.MustCompile(`^(\s*)([A-Za-z0-9_.-]+):(\s.*|)$`)

// safety: a line edit keeps the values file's comments, which a YAML
// round-trip would drop; a path found twice is refused rather than guessed.
func yamlScalar(lines []string, path ...string) (int, string, error) {
	type key struct {
		indent int
		name   string
	}
	var stack []key
	found, value := -1, ""
	for i, line := range lines {
		m := yamlKeyRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		indent := len(m[1])
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, key{indent, m[2]})
		if len(stack) != len(path) {
			continue
		}
		match := true
		for j := range path {
			if stack[j].name != path[j] {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		if found >= 0 {
			return 0, "", fmt.Errorf("%s appears twice", strings.Join(path, "."))
		}
		found, value = i, strings.Trim(strings.TrimSpace(m[3]), `"'`)
	}
	if found < 0 {
		return 0, "", fmt.Errorf("%s not found", strings.Join(path, "."))
	}
	return found, value, nil
}

func setYAMLScalar(lines []string, value string, path ...string) error {
	i, _, err := yamlScalar(lines, path...)
	if err != nil {
		return err
	}
	m := yamlKeyRE.FindStringSubmatch(lines[i])
	lines[i] = m[1] + m[2] + ": " + value
	return nil
}

var (
	controllerImagePath = []string{"controller", "image"}
	launcherImagePath   = []string{"sparkwing-runner-bundle", "launcher", "image"}
	jobImagePath        = []string{"sparkwing-runner-bundle", "launcher", "jobImage"}
)

func yamlChild(path []string, leaf string) []string {
	return append(append([]string{}, path...), leaf)
}

func readValuesPins(values string) (valuesPins, error) {
	lines := strings.Split(values, "\n")
	var pins valuesPins
	for _, item := range []struct {
		path []string
		pin  *imagePin
	}{{controllerImagePath, &pins.Controller}, {launcherImagePath, &pins.Launcher}} {
		_, repo, err := yamlScalar(lines, yamlChild(item.path, "repository")...)
		if err != nil {
			return pins, err
		}
		_, tag, err := yamlScalar(lines, yamlChild(item.path, "tag")...)
		if err != nil {
			return pins, err
		}
		version, digest, ok := strings.Cut(tag, "@")
		if !ok || !strings.HasPrefix(digest, "sha256:") {
			return pins, fmt.Errorf("%s.tag %q does not pin a digest", strings.Join(item.path, "."), tag)
		}
		*item.pin = imagePin{Repository: repo, Tag: version, Digest: digest}
	}
	for leaf, dst := range map[string]*string{"repository": &pins.Job.Repository, "tag": &pins.Job.Tag, "digest": &pins.Job.Digest} {
		_, v, err := yamlScalar(lines, yamlChild(jobImagePath, leaf)...)
		if err != nil {
			return pins, err
		}
		*dst = v
	}
	if pins.Job.Digest != pins.Launcher.Digest {
		return pins, fmt.Errorf("launcher image %s and job image %s differ; pin them together by hand first", pins.Launcher.Digest, pins.Job.Digest)
	}
	return pins, nil
}

func pinValues(values, phase string, want imagePin) (string, error) {
	lines := strings.Split(values, "\n")
	paths := [][]string{controllerImagePath}
	if phase == deployPhaseLauncher {
		paths = [][]string{launcherImagePath, jobImagePath}
	}
	for _, path := range paths {
		_, repo, err := yamlScalar(lines, yamlChild(path, "repository")...)
		if err != nil {
			return "", err
		}
		if repo != want.Repository {
			return "", fmt.Errorf("%s.repository is %s, not %s", strings.Join(path, "."), repo, want.Repository)
		}
		if strings.HasSuffix(path[len(path)-1], "jobImage") {
			if err := setYAMLScalar(lines, want.Tag, yamlChild(path, "tag")...); err != nil {
				return "", err
			}
			if err := setYAMLScalar(lines, want.Digest, yamlChild(path, "digest")...); err != nil {
				return "", err
			}
			continue
		}
		if err := setYAMLScalar(lines, want.Tag+"@"+want.Digest, yamlChild(path, "tag")...); err != nil {
			return "", err
		}
	}
	return strings.Join(lines, "\n"), nil
}

var logLevelRE = regexp.MustCompile(`(?i)\blevel=(WARN|ERROR)\b|"level"\s*:\s*"(WARN|ERROR)"|\s(WARN|ERROR)\s`)

// safety: the bar the manual release window held each rollout to.
func dirtyLogLines(logs string) []string {
	var out []string
	for _, line := range strings.Split(logs, "\n") {
		if logLevelRE.MatchString(line) {
			out = append(out, line)
		}
	}
	return out
}

// safety: an untagged commit's version sorts below the next patch release, and
// the image tags it names are immutable, so one commit maps to one version.
func deployVersion(newest, newestCommit, commit string) (string, error) {
	if newest != "" && newestCommit == commit {
		return newest, nil
	}
	if newest == "" {
		newest = "v0.0.0"
	}
	next, err := bumpVersion(newest, "patch")
	if err != nil {
		return "", err
	}
	if len(commit) < 12 {
		return "", fmt.Errorf("commit %q is not a full hash", commit)
	}
	return next + "-main.g" + commit[:12], nil
}
