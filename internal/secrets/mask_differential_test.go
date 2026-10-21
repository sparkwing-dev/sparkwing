package secrets_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type differentialOperation struct {
	Kind  string
	Index int
}
type differentialCase struct {
	Values, Lists, MergeValues []string
	Order                      []differentialOperation
	Input                      string
	JSONInput                  any
	protected                  []string
}
type differentialResult struct {
	Text string
	JSON any
}

func TestMaskerGeneratedGoTypeScriptParity(t *testing.T) {
	const seed = 0x8a20261021
	rng := rand.New(rand.NewPCG(seed, seed+1))
	cases := make([]differentialCase, 200)
	for i := range cases {
		cases[i] = generateDifferentialCase(rng, i)
	}
	payload, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "node", "../../sdk/typescript/test/masking-differential.ts")
	command.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("TypeScript differential fixture: %v: %s", err, &stderr)
	}
	var results []differentialResult
	if err := json.Unmarshal(output, &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != len(cases) {
		t.Fatalf("got %d results for %d cases", len(results), len(cases))
	}
	for i, test := range cases {
		t.Run(fmt.Sprintf("seed_%x_case_%d", seed, i), func(t *testing.T) {
			masker := secrets.NewMasker()
			chunkRNG := rand.New(rand.NewPCG(seed, uint64(i)))
			for _, operation := range test.Order {
				switch operation.Kind {
				case "value":
					masker.Register(test.Values[operation.Index])
				case "list":
					masker.RegisterList(test.Lists[operation.Index])
				case "merge":
					other := secrets.NewMasker()
					other.Register(test.MergeValues[operation.Index])
					masker.Merge(other)
				}
			}
			text := masker.Mask(test.Input)
			raw, err := json.Marshal(test.JSONInput)
			if err != nil {
				t.Fatal(err)
			}
			var gotJSON any
			if err := json.Unmarshal(masker.MaskJSON(raw), &gotJSON); err != nil {
				t.Fatal(err)
			}
			if text != results[i].Text || !reflect.DeepEqual(gotJSON, results[i].JSON) {
				t.Fatalf("case=%s\nGo text=%q JSON=%#v\nTS text=%q JSON=%#v", mustDifferentialJSON(t, test), text, gotJSON, results[i].Text, results[i].JSON)
			}
			var checkJSON func(any)
			checkJSON = func(value any) {
				switch value := value.(type) {
				case string:
					for _, fragment := range test.protected {
						if strings.Contains(value, fragment) {
							t.Fatalf("JSON retained %q in %q case=%s", fragment, value, mustDifferentialJSON(t, test))
						}
					}
				case []any:
					for _, child := range value {
						checkJSON(child)
					}
				case map[string]any:
					for key, child := range value {
						checkJSON(key)
						checkJSON(child)
					}
				}
			}
			checkJSON(gotJSON)
			checkJSON(results[i].JSON)
			for attempt := 0; attempt < 2; attempt++ {
				stream := masker.Stream()
				var streamed strings.Builder
				for at := 0; at < len(test.Input); {
					next := min(len(test.Input), at+1+chunkRNG.IntN(63))
					streamed.WriteString(stream.Mask(test.Input[at:next], false))
					at = next
				}
				streamed.WriteString(stream.Mask("", true))
				for _, fragment := range test.protected {
					for sink, value := range map[string]string{"Go": text, "TypeScript": results[i].Text, "stream": streamed.String()} {
						if strings.Contains(value, fragment) {
							t.Fatalf("%s retained %q, attempt=%d case=%s output=%q", sink, fragment, attempt, mustDifferentialJSON(t, test), value)
						}
					}
				}
			}
		})
	}
}

func mustDifferentialJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func generateDifferentialCase(rng *rand.Rand, index int) differentialCase {
	token := func() string {
		alphabet := []rune("abCDef019 +/&=\\\"\t\néΩ")
		var out strings.Builder
		for n := 12 + rng.IntN(12); n > 0; n-- {
			out.WriteRune(alphabet[rng.IntN(len(alphabet))])
		}
		return out.String()
	}
	first, second := token(), token()
	test := differentialCase{Values: []string{first, base64.StdEncoding.EncodeToString([]byte(first))}, Lists: []string{second + ", " + token()}, MergeValues: []string{first, base64.StdEncoding.EncodeToString([]byte(first)), token() + "\r\n" + token()}}
	if index%10 == 0 {
		test.Values = append(test.Values, []string{"*", "a", "12", "\\", "é"}[index/10%5])
	}
	for _, registrations := range []struct {
		kind   string
		values []string
	}{{"value", test.Values}, {"list", test.Lists}, {"merge", test.MergeValues}} {
		kind, values := registrations.kind, registrations.values
		for i, value := range values {
			test.Order = append(test.Order, differentialOperation{kind, i})
			literals := []string{value}
			if kind == "list" {
				for _, part := range strings.Split(value, ",") {
					if part = strings.TrimSpace(part); len(part) >= 12 {
						literals = append(literals, part)
					}
				}
			}
			for _, literal := range append([]string(nil), literals...) {
				for _, line := range strings.Split(literal, "\n") {
					line = strings.TrimSuffix(line, "\r")
					if len(line) >= 12 && strings.TrimSpace(line) != "" {
						literals = append(literals, line)
					}
				}
			}
			for _, literal := range literals {
				test.protected = append(test.protected, literal)
				encoded := []string{base64.StdEncoding.EncodeToString([]byte(literal)), base64.RawURLEncoding.EncodeToString([]byte(literal)), url.QueryEscape(literal)}
				quoted, _ := json.Marshal(literal)
				test.protected = append(test.protected, string(quoted[1:len(quoted)-1]))
				for _, form := range encoded {
					if len(form) >= 4 {
						test.protected = append(test.protected, form)
					}
				}
			}
		}
	}
	test.Order = append(test.Order, test.Order[rng.IntN(len(test.Order))])
	rng.Shuffle(len(test.Order), func(i, j int) { test.Order[i], test.Order[j] = test.Order[j], test.Order[i] })
	seen := make(map[string]bool, len(test.protected))
	unique := test.protected[:0]
	for _, fragment := range test.protected {
		if !seen[fragment] {
			seen[fragment] = true
			unique = append(unique, fragment)
		}
	}
	test.protected = unique
	rng.Shuffle(len(test.protected), func(i, j int) { test.protected[i], test.protected[j] = test.protected[j], test.protected[i] })
	test.Input = "before | " + strings.Join(test.protected, " | ") + " | after"
	test.JSONInput = map[string]any{first: []any{test.Input, second, 12345, true, nil}, "ordinary": map[string]any{second: first, "safe": "surrounding"}}
	return test
}
