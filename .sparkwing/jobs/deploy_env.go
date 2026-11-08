package jobs

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
	"golang.org/x/mod/semver"
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
	SmokeProfiles  []string
	ArgoTimeout    time.Duration
	RolloutTimeout time.Duration
	RunTimeout     time.Duration
	SmokeTimeout   time.Duration
	PollInterval   time.Duration
	ToolDir        string
}

// safety: a real deploy holds production credentials, so it refuses a missing
// toolchain instead of trusting the runner's PATH (decision 86). A dry run holds
// none and previews from PATH, as do tests, which leave ToolDir empty.
func (c deployConfig) toolDirInUse(dry bool) (bool, error) {
	if c.ToolDir == "" {
		return false, nil
	}
	if info, err := os.Stat(c.ToolDir); err == nil && info.IsDir() {
		return true, nil
	}
	if dry {
		return false, nil
	}
	return false, fmt.Errorf("deploy toolchain %s is missing: install the pinned deploy tools there as DELIVERY.md's \"Deploy toolchain\" section describes; a real deploy never falls back to PATH", c.ToolDir)
}

// safety: each phase waits for Argo and then the rollout; rollback repeats
// both phases, so the production budget holds a deploy, a smoke and a full
// rollback, and a job timeout cannot cut a rollback short.
func (c deployConfig) phaseWait() time.Duration { return c.ArgoTimeout + c.RolloutTimeout }

func (c deployConfig) rollbackBudget() time.Duration { return 2*c.phaseWait() + 5*time.Minute }

func (c deployConfig) buildBudget() time.Duration { return 45 * time.Minute }

func (c deployConfig) stagingBudget() time.Duration {
	return 2*c.phaseWait() + 15*time.Minute
}

func (c deployConfig) gateBudget() time.Duration { return 2*c.RunTimeout + 15*time.Minute }

func (c deployConfig) productionBudget() time.Duration {
	return 2*c.phaseWait() + c.SmokeTimeout + c.rollbackBudget() + 15*time.Minute
}

type deployParams struct {
	GitopsKey    string
	ReferenceKey string
	StagingToken string
	SmokeToken   string
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
		// safety: retain both runtime indexes with cloud-* and commit-* tags so
		// the registry keeps them outside its rolling 100-image release window.
		ControllerBase: ecr + "/sparkwing-controller@sha256:e3fba647556534dac6af3877ac5cb2b7f3de374eb2fbc4c0000085e5b43264ac",
		RunnerBase:     ecr + "/sparkwing-runner@sha256:055651afe046f9eb44a8d1eba12d560c189057dd04339cad298f6e9c926f1c8d",
		RoleARN:        "arn:aws:iam::633280902600:role/sparkwing-deploy",
		GitopsURL:      "git@github.com:sparkwing-dev/sparkwing-gitops.git",
		GitopsBranch:   "main",
		ReferenceURL:   "git@github.com:sparkwing-dev/sparkwing-ci-smoke.git",
		ReferenceName:  "hello",
		CommitName:     "Sparkwing deploy",
		CommitEmail:    "deploy@sparkwing.dev",
		Params: deployParams{
			GitopsKey:    "/kikd-prod/sparkwing/deploy/sparkwing-gitops-deploy-key",
			ReferenceKey: "/kikd-prod/sparkwing/deploy/ci-smoke-deploy-key",
			StagingToken: "/kikd-prod/sparkwing/deploy/staging-gate-token",
			SmokeToken:   "/kikd-prod/sparkwing/deploy/scan-submit-token",
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
		SmokeProfiles:  []string{"production-smoke"},
		ArgoTimeout:    10 * time.Minute,
		RolloutTimeout: 5 * time.Minute,
		RunTimeout:     45 * time.Minute,
		SmokeTimeout:   20 * time.Minute,
		PollInterval:   5 * time.Second,
		ToolDir:        "/opt/sparkwing-deploy/bin",
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

// safety: rollback uses this run's own changes and schema evidence, so failure cannot revert another deploy or cross a schema upgrade.
type envDeployment struct {
	Environment     string         `json:"environment"`
	Images          deployImages   `json:"images"`
	Previous        valuesPins     `json:"previous"`
	Changes         []gitopsChange `json:"changes"`
	WindowStart     string         `json:"window_start"`
	PreviousSchema  int            `json:"previous_schema"`
	CandidateSchema int            `json:"candidate_schema"`
}

type stagingGateRecord struct {
	Images       deployImages `json:"images"`
	RunID        string       `json:"run_id"`
	GatedAt      string       `json:"gated_at"`
	ExampleRun   string       `json:"example_run"`
	ReferenceRun string       `json:"reference_run"`
}

const (
	deployStoreSchema = "internal/store/schema.sql"
	deployChart       = "charts/sparkwing-full"
	deployDirPrefix   = "sparkwing-deploy-"
)

func sweepStaleDeployDirs(root string, now time.Time, maxAge time.Duration) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), deployDirPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= maxAge {
			continue
		}
		errs = append(errs, removeDeployDir(filepath.Join(root, e.Name())))
	}
	return errors.Join(errs...)
}

// bug: Go module cache directories are read-only. Unlinking their files needs
// writable parent directories; WalkDir leaves symlink targets untouched.
func removeDeployDir(root string) error {
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.Chmod(path, info.Mode()|0o200)
	})
	if err != nil {
		return err
	}
	return os.RemoveAll(root)
}

var fullCommitRE = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// safety: rollback records are data someone else wrote; git accepts a
// recorded commit only as a full commit id, never as an option.
func gitRevision(rev string) (string, error) {
	if !fullCommitRE.MatchString(rev) {
		return "", fmt.Errorf("%q is not a full commit id", rev)
	}
	return rev, nil
}

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

// safety: legacy git-describe stamps sort after their release base and before Go pseudo-versions.
func deployVersionOrder(version string) (string, error) {
	if !semver.IsValid(version) {
		return "", fmt.Errorf("%q is not a version this pipeline can order", version)
	}
	if !serviceBuildStamp.MatchString(version) {
		return version, nil
	}
	base, rest, _ := strings.Cut(version, "-")
	count, _, _ := strings.Cut(rest, "-")
	if count == "0" {
		return base, nil
	}
	next, err := bumpVersion(base, "patch")
	if err != nil {
		return "", err
	}
	return next + "-0.0." + count, nil
}

type renderedDoc struct {
	text      string
	key       string
	kind      string
	namespace string
	name      string
}

func splitRendered(rendered string) ([]renderedDoc, error) {
	var docs []renderedDoc
	for _, part := range regexp.MustCompile(`(?m)^---[ \t]*$`).Split(rendered, -1) {
		var meta struct {
			Kind     string
			Metadata struct{ Name, Namespace string }
		}
		if err := yaml.Unmarshal([]byte(part), &meta); err != nil {
			return nil, err
		}
		key := ""
		if meta.Kind != "" {
			key = meta.Kind + "/" + meta.Metadata.Namespace + "/" + meta.Metadata.Name
		}
		docs = append(docs, renderedDoc{text: part, key: key, kind: meta.Kind, namespace: meta.Metadata.Namespace, name: meta.Metadata.Name})
	}
	return docs, nil
}

// safety: the sparkwing Argo project admits no cluster-scoped kinds, so a
// GitOps commit cannot grant cluster-wide access; kikd-infra applies these.
var clusterScopedKinds = map[string]bool{
	"Namespace": true, "ClusterRole": true, "ClusterRoleBinding": true, "PriorityClass": true,
	"ValidatingAdmissionPolicy": true, "ValidatingAdmissionPolicyBinding": true,
	"MutatingAdmissionPolicy": true, "MutatingAdmissionPolicyBinding": true,
	"ValidatingWebhookConfiguration": true, "MutatingWebhookConfiguration": true,
	"CustomResourceDefinition": true, "StorageClass": true, "IngressClass": true, "PersistentVolume": true,
}

// safety: any other document without a namespace fails the render, so an
// object the list does not know is never dropped or applied to the wrong
// namespace.
func dropClusterScoped(rendered string) (string, []string, error) {
	docs, err := splitRendered(rendered)
	if err != nil {
		return "", nil, err
	}
	var kept, dropped []string
	for _, doc := range docs {
		switch {
		case clusterScopedKinds[doc.kind]:
			dropped = append(dropped, doc.kind+"/"+doc.name)
		case doc.kind != "" && doc.namespace == "":
			return "", nil, fmt.Errorf("%s/%s has no metadata.namespace and is not a known cluster-scoped kind", doc.kind, doc.name)
		default:
			kept = append(kept, doc.text)
		}
	}
	return strings.Join(kept, "---"), dropped, nil
}

// safety: the controller ships alone, so its commit keeps every other object
// exactly as rendered before and takes only the controller Deployment from
// the new render; chart changes to anything else land with the launcher.
func spliceControllerDeployment(previous, next, namespace, deployment string) (string, error) {
	want := "Deployment/" + namespace + "/" + deployment
	oldDocs, err := splitRendered(previous)
	if err != nil {
		return "", fmt.Errorf("parse the current render: %w", err)
	}
	newDocs, err := splitRendered(next)
	if err != nil {
		return "", fmt.Errorf("parse the new render: %w", err)
	}
	var replacement *renderedDoc
	for i := range newDocs {
		if newDocs[i].key == want {
			replacement = &newDocs[i]
		}
	}
	if replacement == nil {
		return "", fmt.Errorf("the new render has no %s", want)
	}
	found := false
	parts := make([]string, len(oldDocs))
	for i, doc := range oldDocs {
		parts[i] = doc.text
		if doc.key == want {
			parts[i], found = replacement.text, true
		}
	}
	if !found {
		return "", fmt.Errorf("the current render has no %s", want)
	}
	return strings.Join(parts, "---"), nil
}

var deploySchemaVersion = regexp.MustCompile(`(?m)^const expectedSchemaVersion = ([0-9]+)$`)

func readDeploySchemaVersion(source string) (int, error) {
	match := deploySchemaVersion.FindStringSubmatch(source)
	if len(match) != 2 {
		return 0, errors.New("cannot establish controller schema version; production was not changed")
	}
	version, err := strconv.Atoi(match[1])
	if err != nil || version <= 0 {
		return 0, errors.New("invalid controller schema version; production was not changed")
	}
	return version, nil
}
