package launcher

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AdmissionConfig holds the per-install values the job namespace policies render into CEL.
type AdmissionConfig struct {
	Namespace, Launcher, RunnerImage, DockerImage string
}

// AdmissionPolicyName names one of the cluster-scoped policies, and its binding, guarding a job namespace.
func AdmissionPolicyName(kind, namespace string) string {
	return "sparkwing-cloud-" + kind + "-" + namespace
}

const (
	podShape = `size(variables.owners) == 1 && variables.owners[0].apiVersion == 'batch/v1' && variables.owners[0].kind == 'Job' && variables.owners[0].?controller.orValue(false) && variables.owners[0].name == variables.labels[?'sparkwing.dev/job'].orValue('') ? 'job' : ` +
		`size(variables.owners) == 0 && object.metadata.name.startsWith('sw-capacity-') ? 'capacity' : ` +
		`size(variables.owners) == 0 && object.metadata.name.startsWith('sw-boundary-') ? 'probe' : 'other'`
	jobLabelKeys  = `['app', 'app.kubernetes.io/managed-by', 'sparkwing.dev/job', 'sparkwing.dev/team', 'batch.kubernetes.io/controller-uid', 'batch.kubernetes.io/job-name', 'controller-uid', 'job-name']`
	workloadEnv   = `['HOME', 'DOCKER_HOST', 'AWS_REGION', 'AWS_DEFAULT_REGION', 'AWS_STS_REGIONAL_ENDPOINTS']`
	jobController = `'system:serviceaccount:kube-system:job-controller'`
	podOwners     = `['system:serviceaccount:kube-system:job-controller', 'system:serviceaccount:kube-system:generic-garbage-collector']`
)

func podValidations() []admissionv1.Validation {
	return []admissionv1.Validation{
		{Expression: `!variables.create || variables.shape != 'other'`, Message: "only Cloud Job, capacity and boundary probe Pods run in the Sparkwing job namespace"},
		{Expression: `!variables.create || variables.spec.?serviceAccountName.orValue('') == '` + ServiceAccount + `' && !variables.spec.?automountServiceAccountToken.orValue(true)`, Message: "Pods use the customer job service account without an API token"},
		{Expression: `!variables.create || !variables.spec.?hostNetwork.orValue(false) && !variables.spec.?hostIPC.orValue(false) && (!variables.spec.?hostPID.orValue(false) || variables.shape == 'probe' && variables.spec.containers[0].name == 'measure') && variables.containers.all(c, c.?ports.orValue([]).all(p, p.?hostPort.orValue(0) == 0))`, Message: "Pods cannot share host namespaces or ports"},
		{Expression: `!variables.create || variables.spec.?volumes.orValue([]).all(v, variables.shape == 'job' && (v.name == 'scratch' && has(v.emptyDir) || v.name == 'bootstrap' && has(v.secret) && v.secret.?secretName.orValue('') == variables.labels[?'sparkwing.dev/job'].orValue('')))`, Message: "Pods mount only their scratch emptyDir and own bootstrap Secret"},
		{Expression: `!variables.create || (variables.shape == 'job' ? size(variables.spec.containers) == 1 && variables.spec.containers[0].name == 'run-node' && variables.spec.containers[0].image == variables.runnerImage && size(variables.inits) == 2 && variables.inits[0].name == 'docker' && variables.inits[0].image == variables.dockerImage && variables.inits[1].name == 'fetch-source' && variables.inits[1].image == variables.runnerImage : size(variables.spec.containers) == 1 && variables.spec.containers[0].image == variables.runnerImage && size(variables.inits) == 0)`, Message: "Pods run only the launcher's container set and pinned images"},
		{Expression: `!variables.create || variables.containers.all(c, !c.?securityContext.?privileged.orValue(false) || variables.shape == 'job' && c.name == 'docker' || variables.shape == 'probe' && c.name == 'measure')`, Message: "only the DinD sidecar and the boundary measurement run privileged"},
		{Expression: `!variables.create || variables.containers.all(c, size(c.?securityContext.?capabilities.?add.orValue([])) == 0)`, Message: "containers cannot add capabilities"},
		{Expression: `!variables.create || variables.containers.all(c, !has(c.lifecycle) && [c.?livenessProbe, c.?readinessProbe, c.?startupProbe].all(p, !p.hasValue() || !has(p.value().exec) || variables.shape == 'job' && c.name == 'docker' && p == c.?startupProbe && p.value().exec.?command.orValue([]) == ` + celList(dockerProbe) + `))`, Message: "containers run no lifecycle hooks or exec probes beyond the Docker sidecar's readiness check"},
		{Expression: `!variables.create || variables.shape != 'job' || size(variables.inits) == 0 || variables.inits[0].?command.orValue([]) == ` + celList(dockerCommand) + ` && variables.inits[0].?args.orValue([]) == ` + celList(dockerArgs), Message: "the Docker sidecar runs only the launcher's dockerd command"},
		{Expression: `!variables.create || variables.containers.all(c, size(c.?envFrom.orValue([])) == 0 && c.?env.orValue([]).all(e, variables.shape == 'job' && e.name in ` + workloadEnv + ` && !has(e.valueFrom)))`, Message: "containers carry only the launcher's environment and EKS region values, never AWS credentials"},
		{Expression: `!variables.create || variables.team != '' && variables.spec.?nodeSelector.orValue({})[?'` + TeamNodeLabel + `'].orValue('') == variables.team && variables.spec.?tolerations.orValue([]).exists(t, t.?key.orValue('') == '` + TeamNodeLabel + `' && t.?operator.orValue('Equal') == 'Equal' && t.?value.orValue('') == variables.team && t.?effect.orValue('') == 'NoSchedule')`, Message: "Pods select and tolerate only their own team's nodes"},
		{Expression: `!variables.create || (variables.shape == 'capacity' ? variables.spec.?nodeName.orValue('') == '' : variables.spec.?nodeName.orValue('') != '')`, Message: "Job and probe Pods are bound to their reserved node; capacity requests are not"},
		{Expression: `!variables.create || (variables.shape == 'job' ? variables.labels.all(k, k in ` + jobLabelKeys + `) && variables.labels[?'app'].orValue('') == 'sparkwing-job' && variables.labels[?'app.kubernetes.io/managed-by'].orValue('') == '` + managedByLauncher + `' && variables.labels[?'sparkwing.dev/job'].orValue('') != '' : variables.shape == 'capacity' ? variables.labels.all(k, k in ['app', 'app.kubernetes.io/managed-by', '` + capacityLabel + `', 'sparkwing.dev/team']) && variables.labels[?'app'].orValue('') == 'sparkwing-job' && variables.labels[?'app.kubernetes.io/managed-by'].orValue('') == '` + managedByLauncher + `' && variables.labels[?'` + capacityLabel + `'].orValue('') == 'true' : variables.labels.all(k, k in ['` + boundaryControlLabel + `', 'app', 'app.kubernetes.io/managed-by', 'sparkwing.dev/job', 'sparkwing.dev/team']))`, Message: "Pods carry exactly the launcher's labels"},
		{Expression: `!variables.create || variables.spec.?readinessGates.orValue([]).all(g, g.conditionType.startsWith('target-health.elbv2.k8s.aws/'))`, Message: "only AWS load balancer readiness gates are allowed"},
	}
}

func policyVariables(c AdmissionConfig, pod string) []admissionv1.Variable {
	return []admissionv1.Variable{
		{Name: "create", Expression: `request.operation == 'CREATE'`},
		{Name: "launcher", Expression: "'" + c.Launcher + "'"},
		{Name: "runnerImage", Expression: "'" + c.RunnerImage + "'"},
		{Name: "dockerImage", Expression: "'" + c.DockerImage + "'"},
		{Name: "labels", Expression: pod + `.metadata.?labels.orValue({})`},
		{Name: "team", Expression: `variables.labels[?'` + TeamLabel + `'].orValue('')`},
		{Name: "spec", Expression: pod + `.spec`},
		{Name: "inits", Expression: `variables.spec.?initContainers.orValue([])`},
		{Name: "containers", Expression: `variables.spec.containers + variables.inits`},
	}
}

func admissionRule(group string, operations []admissionv1.OperationType, resources ...string) admissionv1.NamedRuleWithOperations {
	return admissionv1.NamedRuleWithOperations{RuleWithOperations: admissionv1.RuleWithOperations{Operations: operations, Rule: admissionv1.Rule{APIGroups: []string{group}, APIVersions: []string{"v1"}, Resources: resources, Scope: ptr(admissionv1.NamespacedScope)}}}
}

func admissionMatch(namespace string, rules ...admissionv1.NamedRuleWithOperations) *admissionv1.MatchResources {
	return &admissionv1.MatchResources{
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": namespace}},
		ObjectSelector:    &metav1.LabelSelector{},
		MatchPolicy:       ptr(admissionv1.Equivalent),
		ResourceRules:     rules,
	}
}

func celList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = strconv.Quote(v)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func probeCommands() string {
	var shapes []string
	for name, command := range map[string][]string{"control": listenerCommand, "witness": witnessCommand, "measure": measureCommand} {
		shapes = append(shapes, fmt.Sprintf("variables.spec.containers[0].name == '%s' && variables.spec.containers[0].?command.orValue([]) == %s", name, celList(command)))
	}
	slices.Sort(shapes)
	return "(" + strings.Join(shapes, " || ") + ")"
}

// AdmissionPolicies returns the Pod and Job policies, with their Deny bindings, scoped to the job namespace.
func AdmissionPolicies(c AdmissionConfig) ([]*admissionv1.ValidatingAdmissionPolicy, []*admissionv1.ValidatingAdmissionPolicyBinding) {
	pods := policyVariables(c, "object")
	pods = append(pods, admissionv1.Variable{Name: "owners", Expression: `object.metadata.?ownerReferences.orValue([])`}, admissionv1.Variable{Name: "shape", Expression: podShape})
	podRules := append([]admissionv1.Validation{
		{Expression: `request.operation != 'CONNECT'`, Message: "exec, attach, port-forward and proxy are not allowed in the Sparkwing job namespace"},
		{Expression: `!variables.create || (variables.shape == 'job' ? request.userInfo.username == ` + jobController + ` : request.userInfo.username == variables.launcher)`, Message: "only the launcher and the Job controller create Pods here"},
		{Expression: `request.operation != 'UPDATE' || size(variables.spec.?ephemeralContainers.orValue([])) == 0`, Message: "ephemeral containers are not allowed"},
		{Expression: `request.operation != 'UPDATE' || object.spec == oldObject.spec && variables.labels == oldObject.metadata.?labels.orValue({})`, Message: "Pod specs and labels are immutable here"},
		{Expression: `request.operation != 'UPDATE' || variables.owners == oldObject.metadata.?ownerReferences.orValue([]) || request.userInfo.username in ` + podOwners, Message: "only the Job controller and garbage collector change Pod ownership"},
		{Expression: `!variables.create || variables.shape != 'probe' || ` + probeCommands(), Message: "boundary probes run only the launcher's measurement commands"},
	}, podValidations()...)
	jobs := append(policyVariables(c, "object.spec.template"), admissionv1.Variable{Name: "shape", Expression: `'job'`})
	jobRules := append([]admissionv1.Validation{
		{Expression: `!variables.create || request.userInfo.username == variables.launcher`, Message: "only the launcher creates Jobs here"},
		{Expression: `!variables.create || object.metadata.name == variables.labels[?'sparkwing.dev/job'].orValue('')`, Message: "the Job template names its own Job"},
		{Expression: `object.spec.?parallelism.orValue(1) == 1 && object.spec.?completions.orValue(1) == 1 && object.spec.?backoffLimit.orValue(6) == 0 && !has(object.spec.podFailurePolicy) && !has(object.spec.successPolicy) && object.spec.?completionMode.orValue('NonIndexed') == 'NonIndexed'`, Message: "Jobs run exactly one Pod without retries"},
		{Expression: `request.operation != 'UPDATE' || object.spec.template == oldObject.spec.template && (request.userInfo.username == variables.launcher || object.spec == oldObject.spec && object.metadata.?annotations.orValue({}) == oldObject.metadata.?annotations.orValue({}))`, Message: "only the launcher changes a Job, and never its template"},
	}, podValidations()...)
	writes := []admissionv1.OperationType{admissionv1.Create, admissionv1.Update}
	var policies []*admissionv1.ValidatingAdmissionPolicy
	var bindings []*admissionv1.ValidatingAdmissionPolicyBinding
	for _, p := range []struct {
		kind       string
		rules      []admissionv1.NamedRuleWithOperations
		variables  []admissionv1.Variable
		validation []admissionv1.Validation
	}{
		{"pods", []admissionv1.NamedRuleWithOperations{admissionRule("", writes, "pods", "pods/ephemeralcontainers", "pods/resize"), admissionRule("", []admissionv1.OperationType{admissionv1.Connect}, "pods/exec", "pods/attach", "pods/portforward", "pods/proxy")}, pods, podRules},
		{"jobs", []admissionv1.NamedRuleWithOperations{admissionRule("batch", writes, "jobs")}, jobs, jobRules},
	} {
		name := AdmissionPolicyName(p.kind, c.Namespace)
		policies = append(policies, &admissionv1.ValidatingAdmissionPolicy{
			TypeMeta:   metav1.TypeMeta{APIVersion: "admissionregistration.k8s.io/v1", Kind: "ValidatingAdmissionPolicy"},
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       admissionv1.ValidatingAdmissionPolicySpec{FailurePolicy: ptr(admissionv1.Fail), MatchConstraints: admissionMatch(c.Namespace, p.rules...), Variables: p.variables, Validations: p.validation},
		})
		bindings = append(bindings, &admissionv1.ValidatingAdmissionPolicyBinding{
			TypeMeta:   metav1.TypeMeta{APIVersion: "admissionregistration.k8s.io/v1", Kind: "ValidatingAdmissionPolicyBinding"},
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: admissionv1.ValidatingAdmissionPolicyBindingSpec{PolicyName: name, ValidationActions: []admissionv1.ValidationAction{admissionv1.Deny}, MatchResources: &admissionv1.MatchResources{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": c.Namespace}},
				ObjectSelector:    &metav1.LabelSelector{},
				MatchPolicy:       ptr(admissionv1.Equivalent),
			}},
		})
	}
	return policies, bindings
}

func (l *Launcher) launcherIdentity(ctx context.Context) (string, error) {
	l.mu.Lock()
	identity := l.identity
	l.mu.Unlock()
	if identity != "" {
		return identity, nil
	}
	review, err := l.Kube.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("read launcher identity: %w", err)
	}
	if review.Status.UserInfo.Username == "" {
		return "", errors.New("Kubernetes returned no launcher identity")
	}
	l.mu.Lock()
	l.identity = review.Status.UserInfo.Username
	l.mu.Unlock()
	return review.Status.UserInfo.Username, nil
}

func (l *Launcher) verifyAdmission(ctx context.Context) error {
	identity, err := l.launcherIdentity(ctx)
	if err != nil {
		return err
	}
	policies, bindings := AdmissionPolicies(AdmissionConfig{Namespace: l.Config.Namespace, Launcher: identity, RunnerImage: l.Config.RunnerImage, DockerImage: l.Config.DockerImage})
	api := l.Kube.AdmissionregistrationV1()
	for i, want := range policies {
		policy, err := api.ValidatingAdmissionPolicies().Get(ctx, want.Name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("Cloud admission policy %s: %w", want.Name, err)
		}
		binding, err := api.ValidatingAdmissionPolicyBindings().Get(ctx, want.Name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("Cloud admission binding %s: %w", want.Name, err)
		}
		if !equality.Semantic.DeepEqual(policy.Spec, want.Spec) || !equality.Semantic.DeepEqual(binding.Spec, bindings[i].Spec) {
			return fmt.Errorf("Cloud admission policy %s does not match this launcher's namespace, identity and images", want.Name)
		}
	}
	return nil
}
