package nodeproto

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

var (
	docFields  = jsonFields(reflect.TypeFor[PlanDoc]())
	nodeFields = append(jsonFields(reflect.TypeFor[PlanNode]()), "tools")
)

func jsonFields(t reflect.Type) []string {
	var names []string
	for i := range t.NumField() {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			names = append(names, name)
		}
	}
	return names
}

// SpecHash is the host's hash of one node document: sha256 over its canonical
// JSON, with keys sorted, no insignificant whitespace and spec_hash left out.
// Hashing the decoded JSON rather than [PlanNode] keeps fields this build
// does not know inside the hash. A document that fails the strict decode
// fails with plan_invalid.
func SpecHash(node json.RawMessage) (string, error) {
	v, err := strictObject(node, nodeFields)
	if err != nil {
		return "", err
	}
	delete(v, "spec_hash")
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(strings.TrimSuffix(buf.String(), "\n")))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// StampSpecHashes returns the plan document plan with each node's spec_hash
// set to its [SpecHash], replacing any the SDK sent. Decode the plan the host
// accepts only from what it returns.
func StampSpecHashes(plan []byte) ([]byte, error) {
	doc, err := strictObject(plan, docFields)
	if err != nil {
		return nil, err
	}
	nodes, ok := doc["nodes"].([]any)
	if !ok && doc["nodes"] != nil {
		return nil, invalidPlan("plan nodes are not an array")
	}
	for i, n := range nodes {
		raw, err := json.Marshal(n)
		if err != nil {
			return nil, err
		}
		hash, err := SpecHash(raw)
		if err != nil {
			return nil, fmt.Errorf("plan node %d: %w", i, err)
		}
		n.(map[string]any)["spec_hash"] = hash
	}
	return json.Marshal(doc)
}

func invalidPlan(format string, args ...any) error {
	return &Error{Code: CodePlanInvalid, Message: fmt.Sprintf(format, args...)}
}

// safety: encoding/json matches struct fields by Unicode case folding and
// lets a later duplicate win, so a key like "ſpec_hash" or a repeated key
// could carry a value past the hash. Any repeated key, and any top-level
// key that folds to a known field without being it, is refused.
func strictObject(raw []byte, known []string) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := checkKeys(dec, known); err != nil {
		var perr *Error
		if errors.As(err, &perr) {
			return nil, err
		}
		return nil, invalidPlan("plan document: %v", err)
	}
	var v map[string]any
	dec = json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil || v == nil {
		return nil, invalidPlan("plan document is not an object")
	}
	return v, nil
}

func checkKeys(dec *json.Decoder, known []string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for dec.More() {
		if delim == '{' {
			tok, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := tok.(string)
			if seen[key] {
				return invalidPlan("plan document repeats key %q", key)
			}
			seen[key] = true
			for _, k := range known {
				if k != key && strings.EqualFold(k, key) {
					return invalidPlan("plan document key %q is not %q", key, k)
				}
			}
		}
		if err := checkKeys(dec, nil); err != nil {
			return err
		}
	}
	_, err = dec.Token()
	return err
}
