package jobs

import (
	"strings"
	"testing"
)

func TestSelectJevSweepCandidatesBalancesSemanticAndWrapperChecks(t *testing.T) {
	largeA := mustJevLintFunctions(t, "alpha/service.go", `package alpha
func reconcileAccount(active bool) error {
	if active {
		callOne()
		callTwo()
		callThree()
	}
	return nil
}
`)[0]
	largeA.Source += strings.Repeat(" ", 700)
	largeB := largeA
	largeB.Path = "beta/service.go"
	largeB.Directory = "beta"
	largeB.Identity = "reconcileProject"
	wrapper := mustJevLintFunctions(t, "gamma/wrapper.go", `package gamma
func validateOrder(value string) error { return validate(value) }
`)[0]
	wrapper.Source += strings.Repeat(" ", 100)
	candidates := selectJevSweepCandidates([]jevLintFunction{largeA, largeB, wrapper}, 5)
	if len(candidates) != 3 {
		t.Fatalf("candidates = %#v", candidates)
	}
	if len(candidates[0].Rules) != 5 || len(candidates[1].Rules) != 5 || len(candidates[2].Rules) != 1 || candidates[2].Rules[0] != "unnecessary_indirection" {
		t.Fatalf("candidate rules = %#v", candidates)
	}
}

func TestNewJevSweepRequestKeepsSmellsIndependent(t *testing.T) {
	function := mustJevLintFunctions(t, "orders/orders.go", `package orders
func prepareOrder(value string) error { return save(value) }
`)[0]
	candidates := []jevSweepCandidate{{Function: function, Rules: []string{"mixed_responsibilities", "boundary_leak", "misleading_contract"}}}
	request := newJevSweepRequest(candidates)
	if len(request.State.Functions) != 1 || len(request.Questions) != 3 {
		t.Fatalf("request = %#v", request)
	}
	for _, rule := range candidates[0].Rules {
		if question := request.Questions["function_0_"+rule]; question.Type != "noul" {
			t.Fatalf("question %s = %#v", rule, question)
		}
	}

	high, low := 0.82, 0.31
	response := jevLintResponse{Answers: map[string]jevLintAnswer{
		"function_0_mixed_responsibilities": {Type: "noul", Noul: &high},
		"function_0_boundary_leak":          {Type: "noul", Noul: &low},
		"function_0_misleading_contract":    {Type: "noul", Noul: &low},
	}}
	findings, err := interpretJevSweep(candidates, response)
	if err != nil || len(findings) != 3 || !findings[0].Report || findings[1].Report || findings[2].Report {
		t.Fatalf("findings = %#v, err %v", findings, err)
	}
}

func TestNewJevSweepRequestDefinesDesignPrincipleChecks(t *testing.T) {
	function := mustJevLintFunctions(t, "orders/orders.go", `package orders
func routeOrder(kind string) error { return route(kind) }
`)[0]
	rules := []string{"open_closed_violation", "dependency_inversion", "primitive_obsession", "feature_envy"}
	request := newJevSweepRequest([]jevSweepCandidate{{Function: function, Rules: rules}})
	for _, rule := range rules {
		question := request.Questions["function_0_"+rule]
		if question.Type != "noul" || question.Instructions == "" || len(question.Criteria) != 2 {
			t.Fatalf("question %s = %#v", rule, question)
		}
	}
}

func TestBatchJevSweepCandidatesBoundsCountAndSource(t *testing.T) {
	var candidates []jevSweepCandidate
	for i := 0; i < 5; i++ {
		candidates = append(candidates, jevSweepCandidate{Function: jevLintFunction{Source: strings.Repeat("x", 20<<10)}})
	}
	batches := batchJevSweepCandidates(candidates)
	if len(batches) != 3 || len(batches[0]) != 2 || len(batches[1]) != 2 || len(batches[2]) != 1 {
		t.Fatalf("batches = %#v", batches)
	}
}

func TestSelectJevSweepPairsReservesCrossPackageJudgments(t *testing.T) {
	var pairs []jevLintPair
	for i := 0; i < 8; i++ {
		pairs = append(pairs, jevLintPair{Changed: jevLintFunction{Path: "same.go", Identity: string(rune('a' + i))}})
	}
	for i := 0; i < 4; i++ {
		pairs = append(pairs, jevLintPair{Changed: jevLintFunction{Path: "cross.go", Identity: string(rune('a' + i))}, CrossPackage: true})
	}
	selected := selectJevSweepPairs(pairs, 6)
	cross := 0
	for _, pair := range selected {
		if pair.CrossPackage {
			cross++
		}
	}
	if len(selected) != 6 || cross != 3 {
		t.Fatalf("selected %d pairs with %d cross-package: %#v", len(selected), cross, selected)
	}
}

func TestJevSweepShuffleIsReproducibleAndRotates(t *testing.T) {
	var functions []jevLintFunction
	for i := 0; i < 30; i++ {
		functions = append(functions, jevLintFunction{
			Path: "pkg" + string(rune('a'+i)) + "/file.go", Directory: "pkg" + string(rune('a'+i)),
			Identity: "function" + string(rune('a'+i)), Source: strings.Repeat("x", 300),
		})
	}
	first := selectJevShuffledCandidates(functions, nil, 8, "seed-one")
	repeated := selectJevShuffledCandidates(functions, nil, 8, "seed-one")
	rotated := selectJevShuffledCandidates(functions, nil, 8, "seed-two")
	if labelsOfJevSweepCandidates(first) != labelsOfJevSweepCandidates(repeated) {
		t.Fatalf("same seed changed selection: %#v vs %#v", first, repeated)
	}
	if labelsOfJevSweepCandidates(first) == labelsOfJevSweepCandidates(rotated) {
		t.Fatalf("different seeds kept selection: %#v", first)
	}
	for _, candidate := range first {
		if len(candidate.Rules) != 4 || candidate.Rules[0] != "open_closed_violation" {
			t.Fatalf("shuffled candidate rules = %#v", candidate.Rules)
		}
	}
}

func TestSelectJevShuffledPairsExcludesRankedPairs(t *testing.T) {
	var pool []jevLintPair
	for i := 0; i < 12; i++ {
		pool = append(pool, jevLintPair{
			Changed:      jevLintFunction{Path: "changed.go", Identity: string(rune('a' + i))},
			Candidate:    jevLintFunction{Path: "candidate.go", Identity: string(rune('a' + i))},
			CrossPackage: i%2 == 0,
		})
	}
	ranked := pool[:2]
	selected := selectJevShuffledPairs(pool, ranked, 6, "seed")
	if len(selected) != 6 {
		t.Fatalf("selected = %#v", selected)
	}
	for _, pair := range selected {
		if jevSweepPairSelected(ranked, pair) {
			t.Fatalf("shuffled selection repeated ranked pair: %#v", pair)
		}
	}
}

func labelsOfJevSweepCandidates(candidates []jevSweepCandidate) string {
	var labels []string
	for _, candidate := range candidates {
		labels = append(labels, functionLabel(candidate.Function))
	}
	return strings.Join(labels, "\n")
}
