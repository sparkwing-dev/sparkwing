package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestParseJevLintFunctionsCapturesMethodsAndCalls(t *testing.T) {
	source := []byte(`package sample

type service struct{}

func (s *service) ValidateOrder(order string, strict bool) error {
	return checkOrder(order, strict)
}

`)
	functions, err := parseJevLintFunctions("sample/service.go", source)
	if err != nil {
		t.Fatal(err)
	}
	if len(functions) != 1 {
		t.Fatalf("functions = %d, want 1", len(functions))
	}
	fn := functions[0]
	if fn.Identity != "service.ValidateOrder" || fn.Arity != 2 {
		t.Fatalf("function = %#v", fn)
	}
	for _, token := range []string{"validate", "order"} {
		if _, ok := fn.NameTokens[token]; !ok {
			t.Errorf("name tokens omit %q: %v", token, fn.NameTokens)
		}
	}
	if _, ok := fn.CallTokens["check"]; !ok {
		t.Errorf("call tokens omit check: %v", fn.CallTokens)
	}
}

func TestJevLintIgnoresSymlinkedGoCandidatesOutsideCheckout(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(t.TempDir(), "private.go")
	if err := os.WriteFile(private, []byte("package private\nfunc PrivateSetting() string { return \"fake private marker\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, filepath.Join(root, "linked.go")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "regular.go"), []byte("package regular\nfunc Safe() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	functions, err := collectCurrentGoFunctions(root, []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	if len(functions) != 1 || functions[0].Path != "regular.go" {
		t.Fatalf("Go candidates included a symlink or lost the regular file: %+v", functions)
	}
}

func TestJevLintRefusesUntrackedSymlinkBeforeBuildingTypeSafeRequest(t *testing.T) {
	root := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	runGit("init", "--quiet")
	runGit("-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "base")
	private := filepath.Join(t.TempDir(), "private.go")
	content := "package private\n" + strings.Repeat("// fake private line\n", 110) +
		"func PrivateSetting() string { return \"fake private marker\" }\n"
	if err := os.WriteFile(private, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, filepath.Join(root, "linked.go")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	change, err := collectJevLintChange(t.Context(), root, "HEAD", []string{"linked.go"}, []string{"linked.go"})
	if err == nil {
		if change != nil && strings.Contains(newJevLintChangeRequest(*change).State.Change.Diff, "fake private marker") {
			t.Fatal("private source outside checkout would enter a TypeSafe request")
		}
		t.Fatal("changed symlink was accepted")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("changed symlink refusal = %v", err)
	}
}

func TestJevLintReadStaysInsideCheckoutAcrossParentSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "regular"), 0o700); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(outside, "private.go")
	if err := os.WriteFile(private, []byte("package private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "regular", "safe.go"), []byte("package safe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked-dir")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(private, filepath.Join(root, "linked.go")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, path := range []string{"linked-dir/private.go", "linked.go"} {
		if _, err := readJevLintFile(root, path); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("read %q = %v, want symlink refusal", path, err)
		}
	}
	if body, err := readJevLintFile(root, "regular/safe.go"); err != nil || string(body) != "package safe\n" {
		t.Fatalf("regular checkout file = %q, %v", body, err)
	}
}

func TestJevLintInvariantConfigRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(t.TempDir(), "private.yaml")
	if err := os.WriteFile(private, []byte("version: 1\ninvariants: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, filepath.Join(root, "invariants.yaml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readJevLintInvariants(root, "invariants.yaml"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked invariant config = %v, want refusal", err)
	}
}

func TestRankJevLintPairsPrefersSharedResponsibilitySignals(t *testing.T) {
	changed := mustJevLintFunctions(t, "orders/new.go", `package orders
func validateOrder(order string) error { return checkOrder(order) }
`)[0]
	best := mustJevLintFunctions(t, "orders/existing.go", `package orders
func checkOrderForDispatch(order string) error { return checkOrder(order) }
`)[0]
	incidental := mustJevLintFunctions(t, "orders/log.go", `package orders
func writeLog(message string) error { return storeMessage(message) }
`)[0]

	pairs := rankJevLintPairs([]jevLintFunction{changed}, []jevLintFunction{changed, incidental, best}, 2, jevLintMaxPairSourceBytes)
	if len(pairs) != 1 {
		t.Fatalf("pairs = %d, want only the plausible candidate: %#v", len(pairs), pairs)
	}
	if pairs[0].Candidate.Identity != "checkOrderForDispatch" {
		t.Fatalf("candidate = %s", pairs[0].Candidate.Identity)
	}
}

func TestRankJevLintPairsDoesNotReverseDuplicateChangedPairs(t *testing.T) {
	functions := mustJevLintFunctions(t, "orders/orders.go", `package orders
func validateOrder(order string) error { return checkOrder(order) }
func checkOrderForDispatch(order string) error { return checkOrder(order) }
`)
	pairs := rankJevLintPairs(functions, functions, 6, jevLintMaxPairSourceBytes)
	if len(pairs) != 1 {
		t.Fatalf("pairs = %d, want one unordered pair: %#v", len(pairs), pairs)
	}
}

func TestRankJevLintPairsIncludesStrongCrossPackageCandidate(t *testing.T) {
	changed := mustJevLintFunctions(t, "checkout/price.go", `package checkout
func calculateInvoiceTax(amount int) int { return amount * 7 / 100 }
`)[0]
	candidate := mustJevLintFunctions(t, "billing/tax.go", `package billing
func invoiceTaxAmount(amount int) int { return amount * 7 / 100 }
`)[0]
	pairs := rankJevLintPairs([]jevLintFunction{changed}, []jevLintFunction{changed, candidate}, 2, jevLintMaxPairSourceBytes)
	if len(pairs) != 1 || !pairs[0].CrossPackage {
		t.Fatalf("cross-package pairs = %#v", pairs)
	}
	request := newJevLintRequest(pairs, nil)
	structure := request.Questions["pair_0_structure"]
	if _, ok := structure.Criteria["extract_shared_package"]; !ok || len(structure.Criteria) != 5 {
		t.Fatalf("cross-package structure = %#v", structure)
	}
	same := 0.86
	confidence := 0.30
	response := jevLintResponse{Model: "jev-1.13.0", Answers: map[string]jevLintAnswer{
		"pair_0_same_responsibility": {Type: "noul", Noul: &same},
		"pair_0_structure": {
			Type:       "choice",
			Choice:     "extract_shared_package",
			Confidence: &confidence,
			Probabilities: map[string]float64{
				"keep_separate":             0.05,
				"move_to_changed_package":   0.10,
				"move_to_candidate_package": 0.10,
				"extract_shared_package":    0.30,
				"unclear":                   0.45,
			},
		},
	}}
	if err := validateJevLintResponse(request, response); err != nil {
		t.Fatal(err)
	}
	findings, _, err := interpretJevLint(pairs, nil, response)
	if err != nil || !findings[0].Report {
		t.Fatalf("findings = %#v, err %v", findings, err)
	}
}

func TestRankJevLintPairsStopsAtGoModuleBoundary(t *testing.T) {
	changed := mustJevLintFunctions(t, ".sparkwing/jobs/revision.go", `package jobs
func shortRevision(value string) string { return value[:12] }
`)[0]
	candidate := mustJevLintFunctions(t, "internal/git/revision.go", `package git
func shortRevision(value string) string { return value[:12] }
`)[0]
	changed.Module = ".sparkwing"
	candidate.Module = "."
	pairs := rankJevLintPairs([]jevLintFunction{changed}, []jevLintFunction{changed, candidate}, 2, jevLintMaxPairSourceBytes)
	if len(pairs) != 0 {
		t.Fatalf("cross-module pairs = %#v", pairs)
	}
}

func TestSelectJevLintRulesUsesSyntaxOnlyForCandidateRetrieval(t *testing.T) {
	function := mustJevLintFunctions(t, "checkout/discount.go", `package checkout
func discountEligible(total int, member, blocked bool) bool {
	return total >= 4200 && member && !blocked && "premium" != "disabled"
}
`)[0]
	if function.BooleanTerms != 4 || len(function.Literals) < 2 {
		t.Fatalf("signals = terms %d, literals %v", function.BooleanTerms, function.Literals)
	}
	rules := selectJevLintRules([]jevLintFunction{function}, 3, jevLintMaxRuleSourceBytes)
	if len(rules) != 1 || len(rules[0].Rules) != 2 {
		t.Fatalf("rules = %#v", rules)
	}
	request := newJevLintRequest(nil, rules)
	if len(request.Questions) != 2 || len(request.State.Functions) != 1 {
		t.Fatalf("request = %#v", request)
	}
	complex := 0.82
	magic := 0.79
	response := jevLintResponse{Model: "jev-1.13.0", Answers: map[string]jevLintAnswer{
		"function_0_complex_conditional": {Type: "noul", Noul: &complex},
		"function_0_magic_values":        {Type: "noul", Noul: &magic},
	}}
	if err := validateJevLintResponse(request, response); err != nil {
		t.Fatal(err)
	}
	_, findings, err := interpretJevLint(nil, rules, response)
	if err != nil || len(findings) != 2 || !findings[0].Report || !findings[1].Report {
		t.Fatalf("findings = %#v, err %v", findings, err)
	}
}

func TestNewJevLintRequestKeepsJudgmentsSeparate(t *testing.T) {
	functions := mustJevLintFunctions(t, "orders/orders.go", `package orders
func validateOrder(order string) error { return checkOrder(order) }
func checkOrderForDispatch(order string) error { return checkOrder(order) }
`)
	request := newJevLintRequest([]jevLintPair{{Changed: functions[0], Candidate: functions[1]}}, nil)
	if request.Model != jevLintModel || len(request.State.Pairs) != 1 || len(request.Questions) != 2 {
		t.Fatalf("request = %#v", request)
	}
	if request.Questions["pair_0_same_responsibility"].Type != "noul" {
		t.Error("same-responsibility judgment is not a Noul")
	}
	structure := request.Questions["pair_0_structure"]
	if structure.Type != "choice" || len(structure.Criteria) != 4 {
		t.Fatalf("structure question = %#v", structure)
	}
}

func TestResolveJevLintSendsBearerKeyAndCachesExactRequest(t *testing.T) {
	functions := mustJevLintFunctions(t, "orders/orders.go", `package orders
func validateOrder(order string) error { return checkOrder(order) }
func checkOrderForDispatch(order string) error { return checkOrder(order) }
`)
	request := newJevLintRequest([]jevLintPair{{Changed: functions[0], Candidate: functions[1]}}, nil)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		_ = json.NewEncoder(w).Encode(validJevLintResponse())
	}))
	defer server.Close()
	cache := filepath.Join(t.TempDir(), "cache")

	first, cached, err := resolveJevLint(context.Background(), server.Client(), server.URL, "test-key", request, cache)
	if err != nil || cached {
		t.Fatalf("first resolve = cached %v, err %v", cached, err)
	}
	second, cached, err := resolveJevLint(context.Background(), server.Client(), server.URL, "", request, cache)
	if err != nil || !cached {
		t.Fatalf("second resolve = cached %v, err %v", cached, err)
	}
	if calls != 1 || first.Answers["pair_0_structure"].Choice != second.Answers["pair_0_structure"].Choice {
		t.Fatalf("calls = %d, responses differ", calls)
	}
}

func TestResolveJevLintRequiresKeyForCacheMiss(t *testing.T) {
	functions := mustJevLintFunctions(t, "orders/orders.go", `package orders
func validateOrder(order string) error { return checkOrder(order) }
func checkOrderForDispatch(order string) error { return checkOrder(order) }
`)
	request := newJevLintRequest([]jevLintPair{{Changed: functions[0], Candidate: functions[1]}}, nil)
	_, cached, err := resolveJevLint(context.Background(), http.DefaultClient, "unused", "", request, t.TempDir())
	if err == nil || cached || !strings.Contains(err.Error(), "TYPESAFE_API_KEY") {
		t.Fatalf("resolve = cached %v, err %v", cached, err)
	}
}

func TestRankJevLintPairsIgnoresPipelineCeremony(t *testing.T) {
	functions := mustJevLintFunctions(t, "jobs/pipelines.go", `package jobs
type alpha struct{}
type beta struct{}
func (alpha) Plan() error { return nil }
func (beta) Plan() error { return nil }
`)
	pairs := rankJevLintPairs([]jevLintFunction{functions[0]}, functions, 2, jevLintMaxPairSourceBytes)
	if len(pairs) != 0 {
		t.Fatalf("pipeline ceremony produced candidates: %#v", pairs)
	}
}

func TestRankJevLintPairsIgnoresPlatformImplementationsOfOneFunction(t *testing.T) {
	linux := mustJevLintFunctions(t, "proc/signal_linux.go", `package proc
func signalProcess(pid int) error { return signalUnix(pid) }
`)[0]
	windows := mustJevLintFunctions(t, "proc/signal_windows.go", `package proc
func signalProcess(pid int) error { return signalWindows(pid) }
`)[0]
	pairs := rankJevLintPairs([]jevLintFunction{linux}, []jevLintFunction{linux, windows}, 2, jevLintMaxPairSourceBytes)
	if len(pairs) != 0 {
		t.Fatalf("platform alternatives produced candidates: %#v", pairs)
	}
}

func TestInterpretJevLintRequiresBothJudgments(t *testing.T) {
	functions := mustJevLintFunctions(t, "orders/orders.go", `package orders
func validateOrder(order string) error { return checkOrder(order) }
func checkOrderForDispatch(order string) error { return checkOrder(order) }
`)
	pairs := []jevLintPair{{Changed: functions[0], Candidate: functions[1]}}
	response := validJevLintResponse()
	findings, _, err := interpretJevLint(pairs, nil, response)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || !findings[0].Report {
		t.Fatalf("findings = %#v", findings)
	}

	low := 0.60
	response.Answers["pair_0_same_responsibility"] = jevLintAnswer{Type: "noul", Noul: &low}
	findings, _, err = interpretJevLint(pairs, nil, response)
	if err != nil || findings[0].Report {
		t.Fatalf("low-probability finding reported: %#v, %v", findings, err)
	}

	response = validJevLintResponse()
	lowConfidence := 0.59
	response.Answers["pair_0_structure"] = jevLintAnswer{
		Type:       "choice",
		Choice:     "combine",
		Confidence: &lowConfidence,
	}
	findings, _, err = interpretJevLint(pairs, nil, response)
	if err != nil || findings[0].Report {
		t.Fatalf("low-confidence finding reported: %#v, %v", findings, err)
	}
}

func TestValidateJevLintResponseRejectsIncompleteChoiceDistribution(t *testing.T) {
	functions := mustJevLintFunctions(t, "orders/orders.go", `package orders
func validateOrder(order string) error { return checkOrder(order) }
func checkOrderForDispatch(order string) error { return checkOrder(order) }
`)
	request := newJevLintRequest([]jevLintPair{{Changed: functions[0], Candidate: functions[1]}}, nil)
	response := validJevLintResponse()
	delete(response.Answers["pair_0_structure"].Probabilities, "unclear")
	if err := validateJevLintResponse(request, response); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestValidateJevLintResponseAllowsRoundedChoiceDistribution(t *testing.T) {
	functions := mustJevLintFunctions(t, "orders/orders.go", `package orders
func validateOrder(order string) error { return checkOrder(order) }
func checkOrderForDispatch(order string) error { return checkOrder(order) }
`)
	request := newJevLintRequest([]jevLintPair{{Changed: functions[0], Candidate: functions[1]}}, nil)
	response := validJevLintResponse()
	response.Answers["pair_0_structure"].Probabilities["unclear"] = 0.05
	if err := validateJevLintResponse(request, response); err != nil {
		t.Fatal(err)
	}
}

func TestInterpretJevLintChangeRequiresProbabilityAndDirection(t *testing.T) {
	request := newJevLintChangeRequest(jevLintChangeCandidate{Summary: "8 files, 900 added lines", Diff: "large wrapper"})
	sprawl := 0.62
	confidence := 0.18
	response := jevLintResponse{
		Model: "jev-1.13.0",
		Answers: map[string]jevLintAnswer{
			"change_workaround_sprawl": {Type: "noul", Noul: &sprawl},
			"change_design_direction": {
				Type:       "choice",
				Choice:     "revisit_assumption",
				Confidence: &confidence,
				Probabilities: map[string]float64{
					"keep_approach":           0.18,
					"revisit_assumption":      0.31,
					"replace_with_direct_fix": 0.29,
					"unclear":                 0.22,
				},
			},
		},
	}
	if err := validateJevLintResponse(request, response); err != nil {
		t.Fatal(err)
	}
	finding, err := interpretJevLintChange(response)
	if err != nil || !finding.Report {
		t.Fatalf("finding = %#v, err %v", finding, err)
	}
	response.Answers["change_design_direction"] = jevLintAnswer{
		Type:       "choice",
		Choice:     "keep_approach",
		Confidence: &confidence,
	}
	finding, err = interpretJevLintChange(response)
	if err != nil || finding.Report {
		t.Fatalf("keep finding = %#v, err %v", finding, err)
	}
}

func TestReadJevLintInvariantsRejectsUnknownFields(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "invariants.yaml")
	body := `version: 1
invariants:
  - id: portable
    statement: Pipelines stay portable.
    why: Local execution is a product contract.
    scope: ["sparkwing/**"]
    surprise: true
`
	if err := os.WriteFile(filename, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readJevLintInvariants(filepath.Dir(filename), filepath.Base(filename)); err == nil || !strings.Contains(err.Error(), "field surprise") {
		t.Fatalf("read error = %v", err)
	}
}

func TestSelectJevLintInvariantsUsesScope(t *testing.T) {
	invariants := []jevLintInvariant{
		{ID: "pipelines", Scope: []string{"sparkwing/**", ".sparkwing/**"}},
		{ID: "cli", Scope: []string{"cmd/sparkwing/**"}},
	}
	selected := selectJevLintInvariants(invariants, []string{"sparkwing/pipeline.go", "README.md"})
	if len(selected) != 1 || selected[0].ID != "pipelines" || len(selected[0].Paths) != 1 {
		t.Fatalf("selected = %#v", selected)
	}
	selected = selectJevLintInvariants(invariants, []string{jevLintInvariantFile})
	if len(selected) != 2 || selected[0].Paths[0] != jevLintInvariantFile || selected[1].Paths[0] != jevLintInvariantFile {
		t.Fatalf("config-selected = %#v", selected)
	}
}

func TestJevLintInvariantRequestAsksIndependentQuestionsOverSharedChange(t *testing.T) {
	threshold := 0.80
	invariants := []jevLintInvariant{
		{ID: "portable", Statement: "Pipelines stay portable.", Why: "Local execution matters.", Paths: []string{"sparkwing/pipeline.go"}},
		{ID: "flags", Statement: "Commands use flags.", Why: "Calls stay clear.", Exceptions: []string{"run accepts a pipeline name"}, Threshold: &threshold, Paths: []string{"cmd/sparkwing/main.go"}},
	}
	request := newJevLintInvariantRequest(jevLintInvariantAnalysis{Summary: "two files", Diff: "shared diff", Invariants: invariants})
	if len(request.Questions) != 2 || len(request.State.Invariants) != 2 || request.State.Change.Diff != "shared diff" {
		t.Fatalf("request = %#v", request)
	}
	for i := range invariants {
		question := request.Questions["invariant_"+strconv.Itoa(i)]
		if question.Type != "noul" || !strings.Contains(question.Instructions, fmt.Sprintf("invariants[%d]", i)) {
			t.Fatalf("question %d = %#v", i, question)
		}
	}
	high, belowCustomThreshold := 0.70, 0.75
	findings, err := interpretJevLintInvariants(invariants, jevLintResponse{Answers: map[string]jevLintAnswer{
		"invariant_0": {Type: "noul", Noul: &high},
		"invariant_1": {Type: "noul", Noul: &belowCustomThreshold},
	}})
	if err != nil || len(findings) != 2 || !findings[0].Report || findings[1].Report {
		t.Fatalf("findings = %#v, err %v", findings, err)
	}
}

func mustJevLintFunctions(t *testing.T, path, source string) []jevLintFunction {
	t.Helper()
	functions, err := parseJevLintFunctions(path, []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	return functions
}

func validJevLintResponse() jevLintResponse {
	same := 0.86
	confidence := 0.78
	return jevLintResponse{
		Model: "jev-1.13.0",
		Answers: map[string]jevLintAnswer{
			"pair_0_same_responsibility": {Type: "noul", Noul: &same},
			"pair_0_structure": {
				Type:       "choice",
				Choice:     "extract_shared",
				Confidence: &confidence,
				Probabilities: map[string]float64{
					"keep_separate":  0.08,
					"combine":        0.08,
					"extract_shared": 0.78,
					"unclear":        0.06,
				},
			},
		},
	}
}
