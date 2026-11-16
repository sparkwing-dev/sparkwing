package isolationprobe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// PodRef names an existing pod the kube API grade tries to rewrite.
type PodRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// safety: the placement checks trust the owner references cluster controllers write on pods, so the
// grade tries exactly what a forger needs: creating a pod that claims an owner, and rewriting an
// existing pod's owner references and annotations. Every mutating request is a dry run, so even an
// API that wrongly allowed one changes nothing.
const (
	forgedOwner      = `{"apiVersion":"batch/v1","kind":"Job","name":"isolation-probe-forged","uid":"00000000-0000-4000-8000-000000000000","controller":true,"blockOwnerDeletion":false}`
	forgedAnnotation = `{"sparkwing.dev/isolation-probe":"forged"}`
	forgedPodSpec    = `{"restartPolicy":"Never","automountServiceAccountToken":false,"containers":[{"name":"probe","image":"isolation-probe.invalid/none","command":["true"]}]}`
	imdsBase         = "http://169.254.169.254"
)

func quoted(s string) string {
	data, _ := json.Marshal(s) //nolint:errcheck // a string always marshals
	return string(data)
}

func forgedPod(namespace string) []byte {
	return []byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"generateName":"isolation-probe-","namespace":` + quoted(namespace) +
		`,"annotations":` + forgedAnnotation + `,"ownerReferences":[` + forgedOwner + `]},"spec":` + forgedPodSpec + `}`)
}

func forgedMetadata(p PodRef) []byte {
	return []byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":` + quoted(p.Name) + `,"namespace":` + quoted(p.Namespace) +
		`,"annotations":` + forgedAnnotation + `,"ownerReferences":[` + forgedOwner + `]}}`)
}

// safety: kind says what a credential is and where it came from, never its value, which is only
// ever sent as a bearer token; an untestable credential is one the probe found but cannot present.
type credential struct {
	kind       string
	bearer     string
	untestable bool
}

// safety: missing records anything the grade needed and could not learn, which leaves it unproven
// rather than letting it pass on fewer attempts.
type kubeSpec struct {
	namespaces []string
	pods       []PodRef
	creds      []credential
	missing    []string
}

var tokenFileEnv = []string{"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE"}

// safety: the grade presents every identity a context can find, anonymous first: mounted or
// env-pointed token files under each root and credential-named environment values. AWS keys and a
// readable node role need signing the probe does not do, so they are untestable and leave the grade
// unproven instead of being skipped.
func findCredentials(env, roots []string, readFile func(string) ([]byte, error), nodeRole bool) []credential {
	creds := []credential{{kind: "anonymous"}}
	seen := map[string]bool{}
	add := func(kind, value string) {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 16<<10 || strings.ContainsAny(value, "\r\n") || seen[value] {
			return
		}
		seen[value] = true
		creds = append(creds, credential{kind: kind, bearer: value})
	}
	files := map[string]string{}
	for _, root := range roots {
		files[filepath.Join(root, serviceAccountToken)] = "mounted service-account token"
	}
	awsKeys := false
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		if slices.Contains(tokenFileEnv, key) && value != "" {
			files[value] = "token file named by the environment"
			continue
		}
		up := strings.ToUpper(key)
		switch {
		case strings.HasPrefix(up, "AWS_ACCESS_KEY") || strings.HasPrefix(up, "AWS_SECRET") || up == "AWS_SESSION_TOKEN":
			awsKeys = awsKeys || value != ""
		case credentialNamed(up):
			add("credential-named environment value", value)
		}
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		data, err := readFile(p)
		switch {
		case err == nil:
			add(files[p], string(data))
		case !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrPermission):
			creds = append(creds, credential{kind: files[p] + " that could not be read", untestable: true})
		}
	}
	if awsKeys {
		creds = append(creds, credential{kind: "AWS keys in the environment", untestable: true})
	}
	if nodeRole {
		creds = append(creds, credential{kind: "node role from instance metadata", untestable: true})
	}
	return creds
}

func credentialNamed(upper string) bool {
	for _, name := range credentialNames {
		if strings.Contains(upper, name) {
			return true
		}
	}
	return false
}

// safety: node role credentials need SigV4 signing to become a kube API token, so the probe only
// learns whether instance metadata would hand them out; the IMDS targets judge reaching it at all,
// and the token read here never leaves this function.
func (n network) nodeRoleReadable(ctx context.Context, base string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, base+"/latest/api/token", nil)
	if err != nil {
		return false
	}
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "60")
	resp, err := n.client.Do(req)
	if err != nil {
		return false
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	token := strings.TrimSpace(string(body))
	if rerr != nil || resp.StatusCode != http.StatusOK || token == "" {
		return false
	}
	code, err := n.status(ctx, http.MethodGet, base+"/latest/meta-data/iam/security-credentials/", nil, map[string]string{"X-aws-ec2-metadata-token": token})
	return err == nil && code == http.StatusOK
}

// safety: own is the job's own pod, which the job container learns from its hostname and hands the
// DinD containers in their plan; without it the grade cannot try rewriting that pod and stays
// unproven.
func kubeSpecFor(plan Plan, own string, creds []credential) kubeSpec {
	spec := kubeSpec{namespaces: plan.KubeNamespaces, pods: append([]PodRef(nil), plan.KubePods...), creds: creds}
	self := PodRef{Namespace: plan.JobNamespace, Name: own}
	switch {
	case own == "":
		spec.missing = append(spec.missing, "the job's own pod name")
	case !slices.Contains(spec.pods, self):
		spec.pods = append(spec.pods, self)
	}
	if len(spec.namespaces) == 0 {
		spec.missing = append(spec.missing, "the namespaces to try")
	}
	return spec
}

// safety: besides the forgery attempts, the grade still lists the target namespace's secrets,
// reviews the caller's rules there and submits a dry-run Job that mounts a secret.
func kubeAPIRequests(base string, t Target, spec kubeSpec) []gradeRequest {
	ns := url.PathEscape(t.Namespace)
	rules, _ := json.Marshal(map[string]any{ //nolint:errcheck // a map of strings always marshals
		"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectRulesReview", "spec": map[string]string{"namespace": t.Namespace},
	})
	jsonHeader := map[string]string{"Content-Type": "application/json"}
	reqs := []gradeRequest{
		{http.MethodGet, base + "/api/v1/namespaces/" + ns + "/secrets", nil, nil},
		{http.MethodPost, base + "/apis/authorization.k8s.io/v1/selfsubjectrulesreviews", rules, jsonHeader},
		{http.MethodPost, base + "/apis/batch/v1/namespaces/" + ns + "/jobs?dryRun=All", []byte(dryRunJob), jsonHeader},
	}
	for _, namespace := range spec.namespaces {
		reqs = append(reqs, gradeRequest{http.MethodPost, base + "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods?dryRun=All", forgedPod(namespace), jsonHeader})
	}
	for _, p := range spec.pods {
		path := base + "/api/v1/namespaces/" + url.PathEscape(p.Namespace) + "/pods/" + url.PathEscape(p.Name) + "?dryRun=All"
		reqs = append(reqs,
			gradeRequest{http.MethodPatch, path, forgedMetadata(p), map[string]string{"Content-Type": "application/merge-patch+json"}},
			gradeRequest{http.MethodPut, path, forgedMetadata(p), jsonHeader},
		)
	}
	return reqs
}

// safety: an unproven grade could not show every attempt was refused, because of a credential it
// could not present or something it needed and did not learn.
type errUnproven struct{ what []string }

func (e errUnproven) Error() string { return "refusal unproven for " + strings.Join(e.what, ", ") }

// safety: every request goes out with every credential the context found, anonymous first; only
// refusals pass, and an untestable credential or a missing input leaves the grade unproven.
func (n network) kubeGrade(ctx context.Context, t Target) ([]int, error) {
	if !validName(t.Namespace) {
		return nil, errors.New("a kube API grade needs the namespace it asks about")
	}
	if n.kube == nil {
		return nil, errUnproven{what: []string{"a grade with no plan"}}
	}
	spec := *n.kube
	var codes []int
	var errs []error
	unproven := append([]string(nil), spec.missing...)
	for _, c := range spec.creds {
		if c.untestable {
			unproven = append(unproven, c.kind)
			continue
		}
		for _, r := range kubeAPIRequests("https://"+t.Address, t, spec) {
			header := map[string]string{}
			for k, v := range r.header {
				header[k] = v
			}
			if c.bearer != "" {
				header["Authorization"] = "Bearer " + c.bearer
			}
			code, err := n.status(ctx, r.method, r.url, r.body, header)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			codes = append(codes, code)
		}
	}
	if len(unproven) > 0 {
		errs = append(errs, errUnproven{what: unproven})
	}
	return codes, errors.Join(errs...)
}

// safety: an unproven grade names what it lacked by fixed category, and only a transport failure is
// described as an unanswered request.
func describeGradeError(err error) []string {
	var joined interface{ Unwrap() []error }
	errs := []error{err}
	if errors.As(err, &joined) {
		errs = joined.Unwrap()
	}
	var out []string
	var rest []error
	for _, e := range errs {
		var u errUnproven
		if errors.As(e, &u) {
			out = append(out, "unproven:"+strconv.Quote(strings.Join(u.what, ", ")))
			continue
		}
		rest = append(rest, e)
	}
	if len(rest) > 0 {
		out = append(out, "no-answer:"+classifyDial(errors.Join(rest...)))
	}
	return out
}
