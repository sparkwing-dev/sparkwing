package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const testClusterAdmission = `---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: sparkwing-cloud-capacity
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: [""]
        apiVersions: ["v1"]
        operations: ["UPDATE"]
        resources: ["nodes"]
        scope: Cluster
  validations:
    - expression: "oldObject != null"
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: sparkwing-cloud-capacity
spec:
  policyName: sparkwing-cloud-capacity
  validationActions: [Deny]
`

const (
	testLiveAdmissionPolicy  = `{"metadata":{"resourceVersion":"123","labels":{"argocd":"managed"}},"spec":{"failurePolicy":"Fail","matchConstraints":{"matchPolicy":"Equivalent","namespaceSelector":{},"objectSelector":{},"resourceRules":[{"apiGroups":[""],"apiVersions":["v1"],"operations":["UPDATE"],"resources":["nodes"],"scope":"Cluster"}]},"validations":[{"expression":"oldObject != null"}]}}`
	testLiveAdmissionBinding = `{"spec":{"policyName":"sparkwing-cloud-capacity","validationActions":["Deny"]}}`
)

func TestDeployRejectsClusterAdmissionDriftBeforePush(t *testing.T) {
	for _, tc := range []struct {
		name, kind, live, want string
		err                    error
	}{
		{name: "missing capacity policy", kind: "ValidatingAdmissionPolicy", err: errors.New("NotFound"), want: "NotFound"},
		{name: "missing binding", kind: "ValidatingAdmissionPolicyBinding", err: errors.New("NotFound"), want: "NotFound"},
		{name: "unreadable policy", kind: "ValidatingAdmissionPolicy", err: errors.New("Forbidden"), want: "Forbidden"},
		{name: "policy fails open", kind: "ValidatingAdmissionPolicy", live: strings.Replace(testLiveAdmissionPolicy, "Fail", "Ignore", 1), want: "differs from the chart"},
		{name: "binding only warns", kind: "ValidatingAdmissionPolicyBinding", live: strings.Replace(testLiveAdmissionBinding, "Deny", "Warn", 1), want: "differs from the chart"},
		{name: "binding selects another policy", kind: "ValidatingAdmissionPolicyBinding", live: strings.Replace(testLiveAdmissionBinding, "sparkwing-cloud-capacity", "other", 1), want: "differs from the chart"},
		{name: "policy selector bypass", kind: "ValidatingAdmissionPolicy", live: strings.Replace(testLiveAdmissionPolicy, `"objectSelector":{}`, `"objectSelector":{"matchLabels":{"bypass":"true"}}`, 1), want: "differs from the chart"},
		{name: "invalid response", kind: "ValidatingAdmissionPolicy", live: "{", want: "decode cluster admission"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDeploy(t)
			d := f.deployer(false)
			run := d.tools.run
			d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
				if filepath.Base(c.Name) == "kubectl" && len(c.Args) > 1 && c.Args[0] == "get" && c.Args[1] == tc.kind {
					return tc.live, tc.err
				}
				return run(ctx, c)
			}
			before := strings.Join(f.subjects(), "\n")
			if _, _, err := d.deployTo(t.Context(), f.config().Staging, f.candidate()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("deploy with %s = %v", tc.name, err)
			}
			if after := strings.Join(f.subjects(), "\n"); after != before {
				t.Fatal("pushed a deployment before checking cluster admission")
			}
		})
	}
}
