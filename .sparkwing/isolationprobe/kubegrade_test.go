package isolationprobe

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

type kubeFake struct {
	mu       sync.Mutex
	requests []string
	auth     []string
	answer   func(r *http.Request) int
}

func (f *kubeFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI()+" "+string(body))
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	code := http.StatusForbidden
	if f.answer != nil {
		code = f.answer(r)
	}
	w.WriteHeader(code)
}

func kubeTarget(addr string) Target {
	return Target{Item: "C7", Name: "kube API public endpoint", Address: addr, Expect: ExpectDenied, Listening: true, Grade: GradeKubeAPI, Namespace: "controller-ns"}
}

func gradePlan() Plan {
	return Plan{
		JobNamespace: "jobs-ns", KubeNamespaces: []string{"jobs-ns", "controller-ns"},
		KubePods: []PodRef{{Namespace: "controller-ns", Name: "controller-0"}, {Namespace: "jobs-ns", Name: "peer-probe"}},
	}
}

func gradingNetwork(plan Plan, creds []credential) network {
	n := newNetwork()
	spec := kubeSpecFor(plan, "own-probe", creds)
	n.kube = &spec
	return n
}

// The grade tries exactly what forging placement needs, from every credential: a dry-run pod create
// in the job and controller namespaces, and a dry-run patch and update of the owner references and
// annotations of the job's own pod, a peer's pod and a controller pod. All refused, it passes.
func TestKubeAPIGradeTriesPodForgeryEverywhere(t *testing.T) {
	fake := &kubeFake{}
	srv := httptest.NewTLSServer(fake)
	defer srv.Close()
	n := gradingNetwork(gradePlan(), []credential{{kind: "anonymous"}})
	r := n.probe(t.Context(), ContextJob, kubeTarget(srv.Listener.Addr().String()))
	if r.Verdict != Pass {
		t.Fatalf("all refused = %+v, want pass", r)
	}
	var lines []string
	for _, req := range fake.requests {
		method, rest, _ := strings.Cut(req, " ")
		uri, body, _ := strings.Cut(rest, " ")
		lines = append(lines, method+" "+uri)
		forged := strings.Contains(body, `"ownerReferences":[{"apiVersion":"batch/v1","kind":"Job"`) && strings.Contains(body, `"sparkwing.dev/isolation-probe":"forged"`)
		if strings.Contains(uri, "/pods") && !forged {
			t.Errorf("%s %s does not forge owner references and annotations: %s", method, uri, body)
		}
	}
	for _, want := range []string{
		"POST /api/v1/namespaces/jobs-ns/pods?dryRun=All",
		"POST /api/v1/namespaces/controller-ns/pods?dryRun=All",
		"PATCH /api/v1/namespaces/jobs-ns/pods/own-probe?dryRun=All",
		"PUT /api/v1/namespaces/jobs-ns/pods/own-probe?dryRun=All",
		"PATCH /api/v1/namespaces/jobs-ns/pods/peer-probe?dryRun=All",
		"PUT /api/v1/namespaces/jobs-ns/pods/peer-probe?dryRun=All",
		"PATCH /api/v1/namespaces/controller-ns/pods/controller-0?dryRun=All",
		"PUT /api/v1/namespaces/controller-ns/pods/controller-0?dryRun=All",
		"POST /apis/batch/v1/namespaces/controller-ns/jobs?dryRun=All",
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("grade never tried %s; tried %v", want, lines)
		}
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "GET ") && !strings.HasPrefix(line, "POST /apis/authorization") && !strings.Contains(line, "dryRun=All") {
			t.Errorf("mutating request without a dry run: %s", line)
		}
	}
}

// The review's point: an API can refuse the secrets read, the rules review and the dry-run Job while
// letting a caller create or rewrite pods. Any allowed forgery fails the grade, and an answer that
// does not prove a refusal leaves it unproven.
func TestKubeAPIGradeFailsWhenAnyForgeryIsAllowed(t *testing.T) {
	for name, c := range map[string]struct {
		allow func(r *http.Request) bool
		code  int
		want  Verdict
	}{
		"pod create in the job namespace": {func(r *http.Request) bool {
			return r.Method == http.MethodPost && r.URL.Path == "/api/v1/namespaces/jobs-ns/pods"
		}, http.StatusCreated, Breach},
		"pod create in the controller namespace": {func(r *http.Request) bool {
			return r.Method == http.MethodPost && r.URL.Path == "/api/v1/namespaces/controller-ns/pods"
		}, http.StatusCreated, Breach},
		"patching the job's own pod": {func(r *http.Request) bool {
			return r.Method == http.MethodPatch && r.URL.Path == "/api/v1/namespaces/jobs-ns/pods/own-probe"
		}, http.StatusOK, Breach},
		"updating a peer's pod": {func(r *http.Request) bool {
			return r.Method == http.MethodPut && r.URL.Path == "/api/v1/namespaces/jobs-ns/pods/peer-probe"
		}, http.StatusOK, Breach},
		"a patch that is not refused but fails": {func(r *http.Request) bool {
			return r.Method == http.MethodPatch && r.URL.Path == "/api/v1/namespaces/controller-ns/pods/controller-0"
		}, http.StatusNotFound, Inconclusive},
	} {
		fake := &kubeFake{answer: func(r *http.Request) int {
			if c.allow(r) {
				return c.code
			}
			return http.StatusForbidden
		}}
		srv := httptest.NewTLSServer(fake)
		n := gradingNetwork(gradePlan(), []credential{{kind: "anonymous"}})
		r := n.probe(t.Context(), ContextJob, kubeTarget(srv.Listener.Addr().String()))
		srv.Close()
		if r.Verdict != c.want {
			t.Errorf("%s: %+v, want %s", name, r, c.want)
		}
	}
}

// Every credential the job finds is presented, and the grade fails when any of them may forge; a
// credential the probe cannot present, or the job's own pod name missing, leaves it unproven.
func TestKubeAPIGradeTriesEveryCredentialTheJobFinds(t *testing.T) {
	files := map[string]string{"/" + strings.TrimPrefix(serviceAccountToken, "/"): "mounted-token-value"}
	read := func(path string) ([]byte, error) {
		if v, ok := files[path]; ok {
			return []byte(v), nil
		}
		return nil, fs.ErrNotExist
	}
	creds := findCredentials([]string{"PATH=/usr/bin", "CI_TOKEN=env-token-value"}, []string{"/"}, read, false)
	var kinds []string
	for _, c := range creds {
		kinds = append(kinds, c.kind)
	}
	if strings.Join(kinds, ";") != "anonymous;credential-named environment value;mounted service-account token" {
		t.Fatalf("credentials = %v", kinds)
	}
	fake := &kubeFake{answer: func(r *http.Request) int {
		if r.Header.Get("Authorization") == "Bearer env-token-value" && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pods") {
			return http.StatusCreated
		}
		return http.StatusUnauthorized
	}}
	srv := httptest.NewTLSServer(fake)
	defer srv.Close()
	if r := gradingNetwork(gradePlan(), creds).probe(t.Context(), ContextJob, kubeTarget(srv.Listener.Addr().String())); r.Verdict != Breach {
		t.Fatalf("env credential that may create pods = %+v, want breach", r)
	}
	if !slices.Contains(fake.auth, "Bearer mounted-token-value") || !slices.Contains(fake.auth, "") {
		t.Fatal("grade did not present both the mounted token and anonymous")
	}
	for _, c := range []struct {
		name  string
		creds []credential
		own   string
	}{
		{"AWS keys", findCredentials([]string{"AWS_ACCESS_KEY_ID=x", "AWS_SECRET_ACCESS_KEY=y"}, []string{"/"}, read, false), "own-probe"},
		{"a readable node role", findCredentials(nil, []string{"/"}, read, true), "own-probe"},
		{"no own pod name", []credential{{kind: "anonymous"}}, ""},
	} {
		n := newNetwork()
		spec := kubeSpecFor(gradePlan(), c.own, c.creds)
		n.kube = &spec
		r := n.probe(t.Context(), ContextJob, kubeTarget(srv.Listener.Addr().String()))
		if r.Verdict != Inconclusive || !strings.Contains(r.Outcome, "unproven:") {
			t.Errorf("%s: %+v, want an unproven grade", c.name, r)
		}
		for _, value := range []string{"mounted-token-value", "env-token-value", "AWS_ACCESS_KEY_ID"} {
			if strings.Contains(r.Outcome, value) {
				t.Errorf("%s: outcome %q carries %q", c.name, r.Outcome, value)
			}
		}
	}
	if r := newNetwork().probe(t.Context(), ContextJob, kubeTarget(srv.Listener.Addr().String())); r.Verdict != Inconclusive {
		t.Fatalf("a grade without a plan = %+v, want inconclusive", r)
	}
}

// Every authenticated caller may review its own rules, which is how a real kube-apiserver answered a
// service-account token with no other power. A job must hold no cluster identity, so a credential
// the API accepts for that alone still fails the grade.
func TestKubeAPIGradeFailsAnyCredentialTheAPIAccepts(t *testing.T) {
	fake := &kubeFake{answer: func(r *http.Request) int {
		if r.Header.Get("Authorization") == "Bearer powerless-token" && r.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectrulesreviews" {
			return http.StatusCreated
		}
		return http.StatusForbidden
	}}
	srv := httptest.NewTLSServer(fake)
	defer srv.Close()
	creds := []credential{{kind: "anonymous"}, {kind: "credential-named environment value", bearer: "powerless-token"}}
	if r := gradingNetwork(gradePlan(), creds).probe(t.Context(), ContextJob, kubeTarget(srv.Listener.Addr().String())); r.Verdict != Breach {
		t.Fatalf("a credential allowed only its own rules review = %+v, want breach", r)
	}
}

func TestNodeRoleReadableNeedsInstanceMetadataToHandOutARole(t *testing.T) {
	for name, c := range map[string]struct {
		token, role int
		want        bool
	}{
		"role listed":   {http.StatusOK, http.StatusOK, true},
		"no role":       {http.StatusOK, http.StatusNotFound, false},
		"token refused": {http.StatusForbidden, http.StatusOK, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPut && r.URL.Path == "/latest/api/token":
				w.WriteHeader(c.token)
				_, _ = io.WriteString(w, "imds-token")
			case r.URL.Path == "/latest/meta-data/iam/security-credentials/" && r.Header.Get("X-aws-ec2-metadata-token") == "imds-token":
				w.WriteHeader(c.role)
			default:
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
		got := newNetwork().nodeRoleReadable(t.Context(), srv.URL)
		srv.Close()
		if got != c.want {
			t.Errorf("%s: nodeRoleReadable = %v, want %v", name, got, c.want)
		}
	}
}

func TestKubeSpecAddsTheJobsOwnPodOnce(t *testing.T) {
	plan := gradePlan()
	spec := kubeSpecFor(plan, "own-probe", nil)
	if !slices.Contains(spec.pods, PodRef{Namespace: "jobs-ns", Name: "own-probe"}) || len(spec.missing) != 0 {
		t.Fatalf("spec = %+v", spec)
	}
	plan.KubePods = spec.pods
	if again := kubeSpecFor(plan, "own-probe", nil); len(again.pods) != len(spec.pods) {
		t.Fatalf("the DinD containers' spec added the own pod twice: %+v", again.pods)
	}
	if missing := kubeSpecFor(Plan{JobNamespace: "jobs-ns"}, "", nil); len(missing.missing) != 2 {
		t.Fatalf("missing = %v, want the own pod and the namespaces", missing.missing)
	}
}
