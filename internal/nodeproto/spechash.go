package nodeproto

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// SpecHash is the host's hash of one node document: sha256 over its canonical
// JSON, with keys sorted, no insignificant whitespace and spec_hash left out.
// Hashing the decoded JSON rather than [PlanNode] keeps fields this build
// does not know inside the hash.
func SpecHash(node json.RawMessage) (string, error) {
	var v map[string]any
	dec := json.NewDecoder(bytes.NewReader(node))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil || v == nil {
		return "", fmt.Errorf("%w: node document is not an object", ErrMalformed)
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
// set to its [SpecHash], replacing any the SDK sent.
func StampSpecHashes(plan []byte) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(plan, &doc); err != nil || doc == nil {
		return nil, fmt.Errorf("%w: plan document is not an object", ErrMalformed)
	}
	var nodes []map[string]json.RawMessage
	if err := json.Unmarshal(doc["nodes"], &nodes); err != nil {
		return nil, fmt.Errorf("%w: plan nodes: %w", ErrMalformed, err)
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
		n["spec_hash"] = json.RawMessage(strconv.Quote(hash))
	}
	stamped, err := json.Marshal(nodes)
	if err != nil {
		return nil, err
	}
	doc["nodes"] = stamped
	return json.Marshal(doc)
}
