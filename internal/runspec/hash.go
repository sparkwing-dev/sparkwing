package runspec

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"sort"
	"strings"
)

// CanonicalHash excludes seal bookkeeping, retaining semantic fields in nested user documents.
func CanonicalHash(value any) (Hash, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", errors.New("cannot encode canonical input")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := decodeValue(d)
	if err != nil {
		return "", err
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return "", errors.New("trailing canonical input")
	}
	switch value.(type) {
	case Spec, *Spec:
		if m, ok := v.(map[string]any); ok {
			delete(m, "hash")
			delete(m, "sealed_at")
			if r, ok := m["request"].(map[string]any); ok {
				delete(r, "hash")
				delete(r, "admitted_at")
			}
		}
	case SealedRequest, *SealedRequest:
		if m, ok := v.(map[string]any); ok {
			delete(m, "hash")
			delete(m, "admitted_at")
		}
	}
	raw, err = json.Marshal(v)
	if err != nil {
		return "", errors.New("cannot encode canonical input")
	}
	sum := sha256.Sum256(raw)
	return Hash(hex.EncodeToString(sum[:])), nil
}

func decodeValue(d *json.Decoder) (any, error) {
	token, err := d.Token()
	if err != nil {
		return nil, errors.New("invalid canonical JSON")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		if number, ok := token.(json.Number); ok {
			return canonicalNumber(number), nil
		}
		return token, nil
	}
	switch delim {
	case '{':
		m := map[string]any{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, errors.New("invalid canonical JSON key")
			}
			key, ok := k.(string)
			if !ok {
				return nil, errors.New("invalid canonical JSON key")
			}
			if _, ok := m[key]; ok {
				return nil, errors.New("duplicate canonical JSON key")
			}
			v, err := decodeValue(d)
			if err != nil {
				return nil, err
			}
			m[key] = v
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, errors.New("invalid canonical JSON object")
		}
		return m, nil
	case '[':
		values := []any{}
		for d.More() {
			v, err := decodeValue(d)
			if err != nil {
				return nil, err
			}
			values = append(values, v)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, errors.New("invalid canonical JSON array")
		}
		return values, nil
	default:
		return nil, errors.New("invalid canonical JSON delimiter")
	}
}

// NewEnvironmentSalt returns a fresh salt for one environment snapshot.
func NewEnvironmentSalt() (string, error) {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return "", errors.New("cannot salt environment identity")
	}
	return hex.EncodeToString(salt), nil
}

// EnvironmentDigest leaves captured values in the caller's private storage.
// The salt keeps equal values in different snapshots from sharing a digest a reader could test guesses against.
func EnvironmentDigest(values map[string]string, salt string) (EnvSnapshot, error) {
	if raw, err := hex.DecodeString(salt); err != nil || len(raw) != 32 || hex.EncodeToString(raw) != salt {
		return EnvSnapshot{}, errors.New("invalid environment salt")
	}
	if values == nil {
		values = map[string]string{}
	}
	names := make([]string, 0, len(values))
	for name, value := range values {
		if name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value, 0) {
			return EnvSnapshot{}, errors.New("invalid environment entry")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([][]byte, 0, len(names))
	for _, name := range names {
		entries = append(entries, []byte(name+"="+values[name]))
	}
	hash, err := CanonicalHash(struct {
		Version int      `json:"version"`
		Salt    string   `json:"salt"`
		Entries [][]byte `json:"entries"`
	}{3, salt, entries})
	return EnvSnapshot{Names: names, Salt: salt, Digest: hash}, err
}

func clone[T any](v T) (T, error) {
	var result T
	raw, err := json.Marshal(v)
	if err != nil {
		return result, errors.New("cannot encode run inputs")
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return result, errors.New("cannot copy run inputs")
	}
	return result, nil
}

// safety: decimal normalization avoids float conversion and exponent-sized allocations.
func canonicalNumber(number json.Number) json.Number {
	text := string(number)
	negative := strings.HasPrefix(text, "-")
	text = strings.TrimPrefix(text, "-")
	exponent := new(big.Int)
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		exponent.SetString(strings.TrimPrefix(text[i+1:], "+"), 10)
		text = text[:i]
	}
	if i := strings.IndexByte(text, '.'); i >= 0 {
		exponent.Sub(exponent, big.NewInt(int64(len(text)-i-1)))
		text = text[:i] + text[i+1:]
	}
	text = strings.TrimLeft(text, "0")
	if text == "" {
		return "0"
	}
	trimmed := strings.TrimRight(text, "0")
	exponent.Add(exponent, big.NewInt(int64(len(text)-len(trimmed))))
	text = trimmed
	// safety: only short exponents are expanded to avoid allocating attacker-controlled lengths.
	if exponent.IsInt64() && exponent.Int64() >= 0 && exponent.Int64() <= int64(21-len(text)) {
		text += strings.Repeat("0", int(exponent.Int64()))
	} else if exponent.Sign() != 0 {
		text += "e" + exponent.String()
	}
	if negative {
		text = "-" + text
	}
	return json.Number(text)
}
