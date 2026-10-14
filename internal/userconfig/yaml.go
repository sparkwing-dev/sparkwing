package userconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// FieldError locates invalid configuration without retaining its value.
type FieldError struct {
	File, Key, Problem string
	Line               int
}

func (e *FieldError) Error() string {
	if e.File == "" && e.Line == 0 {
		return e.Key + ": " + e.Problem
	}
	return fmt.Sprintf("%s: %s: line %d: %s", e.File, e.Key, e.Line, e.Problem)
}

// Invalid identifies a typed validation failure for later YAML location lookup.
func Invalid(key, problem string) error { return &FieldError{Key: key, Problem: problem} }

// DecodeYAML validates the complete tagged schema before decoding it. It returns
// the document so semantic errors and declaration origins use the same locations.
func DecodeYAML(file string, raw []byte, out any) (*yaml.Node, error) {
	t := reflect.TypeOf(out)
	if t == nil || t.Kind() != reflect.Pointer || reflect.ValueOf(out).IsNil() {
		return nil, errors.New("YAML destination must be a non-nil pointer")
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return &doc, nil
		}
		return nil, yamlSyntaxError(file, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, &FieldError{File: file, Key: "$", Line: extra.Line, Problem: "expected one YAML document"}
	}
	if len(doc.Content) == 0 {
		return &doc, nil
	}
	if err := checkYAML(doc.Content[0], t.Elem(), "$", map[*yaml.Node]bool{}); err != nil {
		return nil, LocateError(file, &doc, err)
	}
	if err := doc.Decode(out); err != nil {
		return nil, &FieldError{File: file, Key: "$", Line: doc.Content[0].Line, Problem: "invalid YAML value"}
	}
	return &doc, nil
}

func yamlSyntaxError(file string, err error) error {
	line := 1
	if match := regexp.MustCompile(`line (\d+)`).FindStringSubmatch(err.Error()); len(match) == 2 {
		parsed, err := strconv.Atoi(match[1])
		if err == nil {
			line = parsed
		}
	}
	return &FieldError{File: file, Key: "$", Line: line, Problem: "invalid YAML syntax"}
}

func checkYAML(n *yaml.Node, t reflect.Type, key string, active map[*yaml.Node]bool) error {
	bad := func(problem string) error { return &FieldError{Key: key, Line: n.Line, Problem: problem} }
	if active[n] {
		return bad("cyclic YAML alias")
	}
	active[n] = true
	defer delete(active, n)
	if n.Kind == yaml.AliasNode {
		return checkYAML(n.Alias, t, key, active)
	}
	if n.Tag == "!!null" {
		return bad("explicit null is not allowed")
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct, reflect.Map:
		if n.Kind != yaml.MappingNode {
			return bad("expected a mapping")
		}
		fields := map[string]reflect.Type{}
		if t.Kind() == reflect.Struct {
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				name := strings.Split(f.Tag.Get("yaml"), ",")[0]
				if name != "-" {
					if name == "" {
						name = strings.ToLower(f.Name)
					}
					fields[name] = f.Type
				}
			}
		}
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Kind != yaml.ScalarNode || k.Tag == "!!null" {
				return bad("expected a scalar mapping key")
			}
			child := k.Value
			if key != "$" {
				child = key + "." + child
			}
			if seen[k.Value] {
				return &FieldError{Key: child, Line: k.Line, Problem: "duplicate key"}
			}
			seen[k.Value] = true
			if k.Tag == "!!merge" {
				if v.Kind == yaml.SequenceNode {
					for _, item := range v.Content {
						if err := checkYAML(item, t, key, active); err != nil {
							return err
						}
					}
				} else if err := checkYAML(v, t, key, active); err != nil {
					return err
				}
				continue
			}
			var ft reflect.Type
			if t.Kind() == reflect.Map {
				ft = t.Elem()
			} else {
				ft = fields[k.Value]
				if ft == nil {
					return &FieldError{Key: child, Line: k.Line, Problem: "unknown key"}
				}
			}
			if err := checkYAML(v, ft, child, active); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			return bad("expected a sequence")
		}
		for i, v := range n.Content {
			if err := checkYAML(v, t.Elem(), fmt.Sprintf("%s[%d]", key, i), active); err != nil {
				return err
			}
		}
	default:
		if n.Kind != yaml.ScalarNode {
			return bad("expected a scalar")
		}
		value := reflect.New(t).Interface()
		if err := n.Decode(value); err != nil {
			return bad("invalid scalar type or value")
		}
	}
	return nil
}

// LocateError attaches a file and YAML line to a value-free validation error.
func LocateError(file string, doc *yaml.Node, err error) error {
	var field *FieldError
	if !errors.As(err, &field) {
		return &FieldError{File: file, Key: "$", Line: 1, Problem: "invalid configuration"}
	}
	copy := *field
	copy.File = file
	if copy.Line == 0 {
		copy.Line = findLine(doc, copy.Key)
		if copy.Line == 0 {
			copy.Line = 1
		}
	}
	return &copy
}

func findLine(doc *yaml.Node, key string) int {
	if doc == nil {
		return 0
	}
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return 1
		}
		doc = doc.Content[0]
	}
	return fieldLine(doc, "", key)
}

func fieldLine(n *yaml.Node, prefix, key string) int {
	if n.Kind == yaml.AliasNode {
		return fieldLine(n.Alias, prefix, key)
	}
	if prefix == key {
		return n.Line
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			child := k.Value
			if prefix != "" {
				child = prefix + "." + child
			}
			if key == child || strings.HasPrefix(key, child+".") || strings.HasPrefix(key, child+"[") {
				return fieldLine(v, child, key)
			}
		}
		for i := 0; i < len(n.Content); i += 2 {
			if n.Content[i].Tag == "!!merge" {
				merge := n.Content[i+1]
				if merge.Kind == yaml.SequenceNode {
					for _, item := range merge.Content {
						if line := fieldLine(item, prefix, key); line > 0 {
							return line
						}
					}
				} else {
					return fieldLine(merge, prefix, key)
				}
			}
		}
	case yaml.SequenceNode:
		for i, v := range n.Content {
			child := fmt.Sprintf("%s[%d]", prefix, i)
			if key == child || strings.HasPrefix(key, child+".") || strings.HasPrefix(key, child+"[") {
				return fieldLine(v, child, key)
			}
		}
	}
	return n.Line
}

// YAMLField returns a mapping field with YAML alias and merge precedence applied.
// Its node retains the source line used for declaration origins.
func YAMLField(node *yaml.Node, key string) *yaml.Node {
	node = yamlValue(node)
	if node == nil {
		return nil
	}
	if node.Kind == yaml.SequenceNode {
		for _, item := range node.Content {
			if found := YAMLField(item, key); found != nil {
				return found
			}
		}
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return yamlValue(node.Content[i+1])
		}
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Tag == "!!merge" {
			return YAMLField(node.Content[i+1], key)
		}
	}
	return nil
}

func yamlValue(node *yaml.Node) *yaml.Node {
	for node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	return node
}
